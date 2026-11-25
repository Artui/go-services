package aguistore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/aguix"
	_ "modernc.org/sqlite"
)

// clock ticks one second per reading, so every save is ordered.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(time.Second)
	return c.at
}

func newClock() *clock { return &clock{at: time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)} }

func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func openSQL(t *testing.T, path string, opts ...Option) *SQL {
	t.Helper()
	s, err := OpenSQL(context.Background(), openDB(t, path), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// each runs fn against both stores.
func each(t *testing.T, fn func(t *testing.T, s Store)) {
	t.Run("memory", func(t *testing.T) { fn(t, NewMemory(WithClock(newClock().now))) })
	t.Run("sql", func(t *testing.T) {
		fn(t, openSQL(t, filepath.Join(t.TempDir(), "threads.db"), WithClock(newClock().now)))
	})
}

func msg(id, role, text string) aguix.Message {
	return aguix.Message{ID: id, Role: role, Content: aguix.TextContent(text)}
}

func mustSave(t *testing.T, s Store, user, thread string, messages ...aguix.Message) {
	t.Helper()
	if err := s.SaveThread(context.Background(), user, thread, messages); err != nil {
		t.Fatal(err)
	}
}

func list(t *testing.T, s Store, user string, limit int) []Thread {
	t.Helper()
	threads, err := s.ListThreads(context.Background(), user, limit)
	if err != nil {
		t.Fatal(err)
	}
	return threads
}

func TestAThreadReadsBackAsSaved(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		want := []aguix.Message{
			msg("u1", "user", "Fill in my email."),
			{ID: "a1", Role: "assistant", ToolCalls: []aguix.ToolCall{{ID: "c1", Type: "function",
				Function: aguix.FunctionCall{Name: "fill_field", Arguments: `{"field":"email"}`}}}},
			{ID: "t1", Role: "tool", ToolCallID: "c1", Content: aguix.TextContent("ok"),
				Metadata: json.RawMessage(`{"outcome":"failed"}`)},
		}
		mustSave(t, s, "alice", "th1", want...)
		got, ok, err := s.LoadThread(ctx, "alice", "th1")
		if err != nil || !ok {
			t.Fatalf("ok %v err %v", ok, err)
		}
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		if string(g) != string(w) {
			t.Errorf("got  %s\nwant %s", g, w)
		}
		if _, ok, err := s.LoadThread(ctx, "alice", "nope"); ok || err != nil {
			t.Errorf("a missing thread: ok %v err %v", ok, err)
		}
	})
}

func TestAnotherUserCannotReachAThread(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		mustSave(t, s, "alice", "th1", msg("u1", "user", "Secret."))
		if m, ok, err := s.LoadThread(ctx, "bob", "th1"); ok || err != nil || m != nil {
			t.Errorf("bob read alice's thread: %v %v %v", m, ok, err)
		}
		if got := list(t, s, "bob", 10); len(got) != 0 {
			t.Errorf("bob lists %v", got)
		}
		if err := s.RenameThread(ctx, "bob", "th1", "Mine"); !errors.Is(err, services.ErrNotFound) {
			t.Errorf("bob's rename: %v", err)
		}
		if err := s.DeleteThread(ctx, "bob", "th1"); err != nil {
			t.Errorf("bob's delete: %v", err)
		}
		// Bob's own thread of the same id is his alone.
		mustSave(t, s, "bob", "th1", msg("u2", "user", "Mine."))
		got := list(t, s, "alice", 10)
		if len(got) != 1 || got[0].Title != "Secret." {
			t.Errorf("alice's threads after bob's attempts: %+v", got)
		}
		m, _, _ := s.LoadThread(ctx, "alice", "th1")
		if len(m) != 1 || m[0].Text() != "Secret." {
			t.Errorf("alice's messages: %+v", m)
		}
	})
}

func TestATitleIsSetOnceAndARenameSurvivesTheNextSave(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		mustSave(t, s, "alice", "th1", msg("u1", "user", "  First\n  question  "))
		mustSave(t, s, "alice", "th1", msg("u0", "user", "Changed."), msg("a1", "assistant", "An answer."))
		if got := list(t, s, "alice", 10); got[0].Title != "First question" || got[0].Preview != "An answer." {
			t.Errorf("after a second save: %+v", got[0])
		}
		if err := s.RenameThread(ctx, "alice", "th1", "Renamed"); err != nil {
			t.Fatal(err)
		}
		mustSave(t, s, "alice", "th1", msg("u1", "user", "First question"), msg("a2", "assistant", "Later."))
		if got := list(t, s, "alice", 10); got[0].Title != "Renamed" || got[0].Preview != "Later." {
			t.Errorf("after a rename and a save: %+v", got[0])
		}
	})
}

func TestTheListIsNewestFirstUpToItsLimit(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		for _, id := range []string{"a", "b", "c"} {
			mustSave(t, s, "alice", id, msg(id, "user", id))
		}
		mustSave(t, s, "alice", "a", msg("a", "user", "a again"))
		var got []string
		for _, th := range list(t, s, "alice", 2) {
			got = append(got, th.ID)
		}
		if !slices.Equal(got, []string{"a", "c"}) {
			t.Errorf("listed %v", got)
		}
		if n := len(list(t, s, "alice", -1)); n != 0 {
			t.Errorf("a negative limit listed %d", n)
		}
		if first := list(t, s, "alice", 1)[0]; !first.UpdatedAt.Equal(time.Date(2026, 11, 25, 9, 0, 4, 0, time.UTC)) {
			t.Errorf("updated at %v", first.UpdatedAt)
		}
	})
}

func TestAThreadIsDeleted(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		mustSave(t, s, "alice", "th1", msg("u1", "user", "Hi."))
		if err := s.DeleteThread(ctx, "alice", "th1"); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.LoadThread(ctx, "alice", "th1"); ok {
			t.Error("the thread is still there")
		}
		if err := s.DeleteThread(ctx, "alice", "th1"); err != nil {
			t.Errorf("deleting it again: %v", err)
		}
	})
}

func TestAThreadThatDoesNotEncodeIsRefused(t *testing.T) {
	each(t, func(t *testing.T, s Store) {
		bad := aguix.Message{ID: "u1", Role: "user", Content: json.RawMessage("{")}
		err := s.SaveThread(context.Background(), "alice", "th1", []aguix.Message{bad})
		if err == nil || !strings.HasPrefix(err.Error(), "aguistore: encoding the thread") {
			t.Errorf("err %v", err)
		}
	})
}

func TestTitlesAndPreviewsReadTheTextAsTheClientDoes(t *testing.T) {
	long := strings.Repeat("é", 70)
	parts := aguix.Message{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"in parts"}]`)}
	for _, tc := range []struct {
		messages       []aguix.Message
		title, preview string
	}{
		{nil, "New conversation", ""},
		{[]aguix.Message{parts, msg("a", "assistant", "  ")}, "New conversation", ""},
		{[]aguix.Message{msg("u", "user", long)}, strings.Repeat("é", 59) + "…", long},
		{[]aguix.Message{msg("u", "user", "Hi."), msg("t", "tool", strings.Repeat("ab ", 50))},
			"Hi.", strings.TrimSpace(strings.Repeat("ab ", 40)) + "…"},
		{[]aguix.Message{msg("a", "assistant", "Hello."), msg("u", "user", "Q?"), msg("b", "assistant", " ")}, "Q?", "Q?"},
		{[]aguix.Message{msg("u", "user", strings.Repeat("x", 60))}, strings.Repeat("x", 60), strings.Repeat("x", 60)},
		{[]aguix.Message{msg("u", "user", strings.Repeat("x", 58)+" yz")}, strings.Repeat("x", 58) + "…",
			strings.Repeat("x", 58) + " yz"},
	} {
		if got := titleFrom(tc.messages); got != tc.title {
			t.Errorf("title %q, want %q", got, tc.title)
		}
		if got := previewFrom(tc.messages); got != tc.preview {
			t.Errorf("preview %q, want %q", got, tc.preview)
		}
	}
}

func TestThreadsSavedAtTheSameInstantListByID(t *testing.T) {
	frozen := WithClock(func() time.Time { return time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC) })
	for name, s := range map[string]Store{
		"memory": NewMemory(frozen),
		"sql":    openSQL(t, filepath.Join(t.TempDir(), "threads.db"), frozen),
	} {
		for _, id := range []string{"b", "c", "a"} {
			mustSave(t, s, "alice", id, msg("u-"+id, "user", id))
		}
		got, err := s.ListThreads(context.Background(), "alice", 10)
		if err != nil || len(got) != 3 || got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "c" {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
}
