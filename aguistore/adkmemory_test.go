package aguistore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"iter"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// fakeSession is a session.Session over a fixed list of events; nil events is
// a session that has none to list.
type fakeSession struct {
	app, user, id string
	events        []*session.Event
}

func (s fakeSession) ID() string                { return s.id }
func (s fakeSession) AppName() string           { return s.app }
func (s fakeSession) UserID() string            { return s.user }
func (s fakeSession) State() session.State      { return nil }
func (s fakeSession) LastUpdateTime() time.Time { return time.Time{} }
func (s fakeSession) Events() session.Events {
	if s.events == nil {
		return nil
	}
	return eventList(s.events)
}

type eventList []*session.Event

func (l eventList) All() iter.Seq[*session.Event] { return slices.Values(l) }
func (l eventList) Len() int                      { return len(l) }
func (l eventList) At(i int) *session.Event       { return l[i] }

var memoryEpoch = time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)

func said(id, author, text string, minute int) *session.Event {
	role := genai.Role(genai.RoleModel)
	if author == "user" {
		role = genai.RoleUser
	}
	return &session.Event{
		ID: id, Author: author, Timestamp: memoryEpoch.Add(time.Duration(minute) * time.Minute),
		LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(text, role)},
	}
}

func remember(t *testing.T, m *ADKMemory, s fakeSession) {
	t.Helper()
	if err := m.AddSessionToMemory(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func recall(t *testing.T, m *ADKMemory, app, user, query string) []string {
	t.Helper()
	res, err := m.SearchMemory(context.Background(), &memory.SearchRequest{AppName: app, UserID: user, Query: query})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range res.Memories {
		var texts []string
		for _, p := range e.Content.Parts {
			texts = append(texts, p.Text)
		}
		out = append(out, strings.Join(texts, "|"))
	}
	return out
}

// forEachMemoryDialect runs fn on an empty memory table in every dialect forEachDialect reaches.
func forEachMemoryDialect(t *testing.T, fn func(t *testing.T, m *ADKMemory)) {
	forEachDialect(t, func(t *testing.T, s *SQL) {
		m, err := OpenADKMemory(context.Background(), s.db, WithDialect(s.cfg.dialect))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`DELETE FROM aguistore_memory`); err != nil {
			t.Fatal(err)
		}
		fn(t, m)
	})
}

func TestEveryDialectRecallsOnlyTheAskingUsersMemory(t *testing.T) {
	forEachMemoryDialect(t, func(t *testing.T, m *ADKMemory) {
		remember(t, m, fakeSession{"app", "alice", "th1", []*session.Event{
			said("e1", "user", "My locker code is 4417.", 0), said("e2", "librarian", "Noted.", 1),
		}})
		remember(t, m, fakeSession{"app", "bob", "th1", []*session.Event{said("e1", "user", "My locker code is 9000.", 0)}})
		remember(t, m, fakeSession{"app", "Alice", "th1", []*session.Event{said("e1", "user", "Locker code 1234.", 0)}})
		remember(t, m, fakeSession{"other", "alice", "th1", []*session.Event{said("e1", "user", "Locker code 5555.", 0)}})
		for user, want := range map[string][]string{
			"alice": {"My locker code is 4417."},
			"bob":   {"My locker code is 9000."},
			"Alice": {"Locker code 1234."},
			"carol": nil,
		} {
			if got := recall(t, m, "app", user, "what is my LOCKER code?"); !slices.Equal(got, want) {
				t.Errorf("%s recalls %q", user, got)
			}
		}
		if got := recall(t, m, "app", "alice", " ?! "); got != nil {
			t.Errorf("a query with no words recalls %q", got)
		}
	})
}

func TestEveryDialectsMemoryReplacesASessionAddedAgain(t *testing.T) {
	forEachMemoryDialect(t, func(t *testing.T, m *ADKMemory) {
		first := fakeSession{"app", "alice", "th1", []*session.Event{
			said("e1", "user", "Book one about owls.", 0), said("e2", "librarian", "Owls are on shelf 3.", 1),
		}}
		remember(t, m, first)
		remember(t, m, first)
		if got := recall(t, m, "app", "alice", "owls"); len(got) != 2 {
			t.Fatalf("a session added twice recalls %q", got)
		}
		remember(t, m, fakeSession{"app", "alice", "th1", []*session.Event{said("e9", "user", "Only herons now.", 0)}})
		if got := recall(t, m, "app", "alice", "owls herons"); !slices.Equal(got, []string{"Only herons now."}) {
			t.Errorf("after the replacement %q", got)
		}
		remember(t, m, fakeSession{"app", "alice", "th2", nil})
		if got := recall(t, m, "app", "alice", "herons"); len(got) != 1 {
			t.Errorf("an empty session disturbed another: %q", got)
		}
	})
}

func TestMemoryRanksByWordsSharedAndCapsAtTen(t *testing.T) {
	m := openMemory(t)
	var events []*session.Event
	for i := range 12 {
		events = append(events, said(fmt.Sprintf("e%d", i), "user", fmt.Sprintf("owl %d", i), i))
	}
	events = append(events, said("best", "user", "owl and heron", 20), said("none", "user", "nothing here", 21))
	remember(t, m, fakeSession{"app", "alice", "th1", events})
	got := recall(t, m, "app", "alice", "owl heron")
	if len(got) != maxMemories || got[0] != "owl and heron" || got[1] != "owl 0" || got[9] != "owl 8" {
		t.Errorf("recalled %q", got)
	}
}

func TestMemoryLeavesThoughtsOutAndKeepsTheEntrysFields(t *testing.T) {
	m := openMemory(t)
	thinking := &session.Event{ID: "t1", Author: "librarian", LLMResponse: model.LLMResponse{Content: &genai.Content{
		Role: genai.RoleModel, Parts: []*genai.Part{{Text: "secret owl plan", Thought: true}, nil},
	}}}
	mixed := said("m1", "librarian", "Owls nest in barns.", 5)
	mixed.Content.Parts = append([]*genai.Part{{Text: "private owl musing", Thought: true}}, mixed.Content.Parts...)
	mixed.CustomMetadata = map[string]any{"shelf": "3"}
	remember(t, m, fakeSession{"app", "alice", "th1", []*session.Event{nil, {ID: "x"}, thinking, mixed}})
	res, err := m.SearchMemory(context.Background(), &memory.SearchRequest{AppName: "app", UserID: "alice", Query: "owl owls"})
	if err != nil || len(res.Memories) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	e := res.Memories[0]
	if e.ID != "m1" || e.Author != "librarian" || !e.Timestamp.Equal(memoryEpoch.Add(5*time.Minute)) ||
		e.CustomMetadata["shelf"] != "3" || len(e.Content.Parts) != 1 || e.Content.Parts[0].Text != "Owls nest in barns." ||
		e.Content.Role != genai.RoleModel {
		t.Errorf("entry %+v content %+v", e, e.Content)
	}
}

func openMemory(t *testing.T) *ADKMemory {
	t.Helper()
	m, err := OpenADKMemory(context.Background(), openDB(t, filepath.Join(t.TempDir(), "memory.db")))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMemoryErrorsAreReported(t *testing.T) {
	ctx := context.Background()
	m := openMemory(t)
	if err := m.AddSessionToMemory(ctx, nil); err == nil {
		t.Error("a nil session was added")
	}
	if _, err := m.SearchMemory(ctx, nil); err == nil {
		t.Error("a nil request was searched")
	}
	odd := said("e1", "user", "owl", 0)
	odd.CustomMetadata = map[string]any{"ch": make(chan int)}
	if err := m.AddSessionToMemory(ctx, fakeSession{"app", "alice", "th1", []*session.Event{odd}}); err == nil ||
		!strings.HasPrefix(err.Error(), "aguistore: encoding the session's memory:") {
		t.Errorf("unencodable metadata: %v", err)
	}
	call := said("e1", "user", "owl", 0)
	call.Content.Parts = append(call.Content.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{
		Name: "f", Args: map[string]any{"ch": make(chan int)},
	}})
	if err := m.AddSessionToMemory(ctx, fakeSession{"app", "alice", "th1", []*session.Event{call}}); err == nil ||
		!strings.HasPrefix(err.Error(), "aguistore: encoding the session's memory:") {
		t.Errorf("unencodable content: %v", err)
	}
	for name, row := range map[string]string{
		"content":  `('app', 'alice', 'bad', 0, 'e', 'a', 0, '{', 'null')`,
		"metadata": `('app', 'alice', 'bad', 0, 'e', 'a', 0, '{"parts":[{"text":"owl"}]}', '[')`,
	} {
		if _, err := m.db.ExecContext(ctx, `DELETE FROM aguistore_memory`); err != nil {
			t.Fatal(err)
		}
		if _, err := m.db.ExecContext(ctx, `INSERT INTO aguistore_memory VALUES `+row); err != nil {
			t.Fatal(err)
		}
		if _, err := m.SearchMemory(ctx, &memory.SearchRequest{AppName: "app", UserID: "alice", Query: "owl"}); err == nil ||
			!strings.HasPrefix(err.Error(), "aguistore: decoding a memory:") {
			t.Errorf("a corrupt %s: %v", name, err)
		}
	}
	if _, err := m.db.ExecContext(ctx, `UPDATE aguistore_memory SET at = 'soon'`); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SearchMemory(ctx, &memory.SearchRequest{AppName: "app", UserID: "alice", Query: "owl"}); err == nil ||
		!strings.HasPrefix(err.Error(), "aguistore: searching memory:") {
		t.Errorf("an unscannable row: %v", err)
	}

	if err := m.db.Close(); err != nil {
		t.Fatal(err)
	}
	one := fakeSession{"app", "alice", "th1", []*session.Event{said("e1", "user", "owl", 0)}}
	if err := m.AddSessionToMemory(ctx, one); err == nil || !strings.HasPrefix(err.Error(), "aguistore: adding to memory:") {
		t.Errorf("adding on a closed database: %v", err)
	}
	if _, err := m.SearchMemory(ctx, &memory.SearchRequest{Query: "owl"}); err == nil ||
		!strings.HasPrefix(err.Error(), "aguistore: searching memory:") {
		t.Errorf("searching on a closed database: %v", err)
	}
	if _, err := OpenADKMemory(ctx, m.db); err == nil || !strings.HasPrefix(err.Error(), "aguistore: creating the memory table:") {
		t.Errorf("opening on a closed database: %v", err)
	}
}

func TestAMemoryWriteThatFailsInsideItsTransactionIsAnError(t *testing.T) {
	ctx := context.Background()
	one := fakeSession{"app", "alice", "th1", []*session.Event{said("e1", "user", "owl", 0)}}
	for name, c := range map[string]struct {
		table string
		s     fakeSession
	}{
		"insert": {`CREATE TABLE aguistore_memory (app_name TEXT)`, one},
		"delete": {`CREATE TABLE aguistore_memory (app_name TEXT, user_id TEXT, session_id TEXT)`, fakeSession{"app", "alice", "th1", nil}},
	} {
		db := openDB(t, filepath.Join(t.TempDir(), "memory.db"))
		if _, err := db.ExecContext(ctx, c.table); err != nil {
			t.Fatal(err)
		}
		m, err := OpenADKMemory(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.AddSessionToMemory(ctx, c.s); err == nil || !strings.HasPrefix(err.Error(), "aguistore: adding to memory:") {
			t.Errorf("%s: %v", name, err)
		}
	}
	m := &ADKMemory{db: sql.OpenDB(memoryConn{})}
	if err := m.AddSessionToMemory(ctx, one); !errors.Is(err, errCommit) {
		t.Errorf("commit: %v", err)
	}
	if _, err := m.SearchMemory(ctx, &memory.SearchRequest{Query: "owl"}); !errors.Is(err, io.ErrUnexpectedEOF) ||
		!strings.HasPrefix(err.Error(), "aguistore: searching memory:") {
		t.Errorf("a search that fails mid-stream: %v", err)
	}
}

var errCommit = errors.New("commit refused")

// memoryConn is a driver whose writes succeed until the commit, which fails,
// and whose queries answer one memory row and then fail.
type memoryConn struct{}

func (memoryConn) Connect(context.Context) (driver.Conn, error) { return memoryConn{}, nil }
func (memoryConn) Driver() driver.Driver                        { return nil }
func (memoryConn) Prepare(string) (driver.Stmt, error)          { return memoryConn{}, nil }
func (memoryConn) Close() error                                 { return nil }
func (memoryConn) Begin() (driver.Tx, error)                    { return memoryConn{}, nil }
func (memoryConn) Commit() error                                { return errCommit }
func (memoryConn) Rollback() error                              { return nil }
func (memoryConn) NumInput() int                                { return -1 }
func (memoryConn) Exec([]driver.Value) (driver.Result, error)   { return driver.RowsAffected(1), nil }
func (memoryConn) Query([]driver.Value) (driver.Rows, error)    { return &memoryStream{}, nil }

type memoryStream struct{ sent bool }

func (*memoryStream) Columns() []string {
	return []string{"event_id", "author", "at", "content", "metadata"}
}
func (*memoryStream) Close() error { return nil }
func (r *memoryStream) Next(dest []driver.Value) error {
	if r.sent {
		return io.ErrUnexpectedEOF
	}
	r.sent = true
	copy(dest, []driver.Value{"e1", "user", int64(0), `{"parts":[{"text":"owl"}]}`, "null"})
	return nil
}
