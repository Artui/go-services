package aguiadk

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/genai"
)

// memStore is a ThreadStore keyed by user and thread, with failures to order.
type memStore struct {
	mu      sync.Mutex
	threads map[[2]string][]aguix.Message
	saves   int
	loadErr error
	saveErr error
	saveCtx error
}

func newMemStore() *memStore { return &memStore{threads: map[[2]string][]aguix.Message{}} }

func (s *memStore) LoadThread(_ context.Context, user, thread string) ([]aguix.Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, false, s.loadErr
	}
	m, ok := s.threads[[2]string{user, thread}]
	return slices.Clone(m), ok, nil
}

func (s *memStore) SaveThread(ctx context.Context, user, thread string, messages []aguix.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	s.saveCtx = ctx.Err()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.threads[[2]string{user, thread}] = slices.Clone(messages)
	return nil
}

func (s *memStore) thread(user, thread string) []aguix.Message {
	m, _, _ := s.LoadThread(context.Background(), user, thread)
	return m
}

func ids(messages []aguix.Message) []string {
	var out []string
	for _, m := range messages {
		out = append(out, m.Role+":"+m.ID)
	}
	return out
}

func asUser(id string) Option {
	return WithUserID(func(context.Context) (string, error) { return id, nil })
}

func TestABrowserAnswerIsStoredAsAContinuationOfItsThread(t *testing.T) {
	store := newMemStore()
	llm := &scriptLLM{steps: []step{
		{text: "Filling it in.", calls: []*genai.FunctionCall{fillCall("c1")}},
		{text: "Done."},
	}}
	bridge := mustNew(t, librarian(t, llm, ClientToolset()), WithThreadStore(store))
	first := input("run-1", text("user", "Fill in my email."))
	first.Tools = []aguix.ToolDefinition{fillField}
	if o := outcome(t, serve(t, bridge, first)); o["pendingToolCallIds"] == nil {
		t.Fatalf("the first run left nothing pending: %v", o)
	}
	stored := store.thread("anonymous", "thread-1")
	if got := ids(stored); !slices.Equal(got, []string{"user:user-Fill in my email.", "assistant:run-1-1"}) {
		t.Fatalf("after the first run the thread holds %v", got)
	}

	// The page answers the call: a tool message and no user turn.
	answer := aguix.Message{ID: "t1", Role: "tool", ToolCallID: "c1", Content: aguix.TextContent(`{"ok":true}`)}
	second := input("run-2", append(stored, answer)...)
	second.Tools = first.Tools
	if s := serve(t, bridge, second); s.last()["type"] != "RUN_FINISHED" {
		t.Fatalf("the answering run failed:\n%s", s.body)
	}
	got := store.thread("anonymous", "thread-1")
	want := []string{"user:user-Fill in my email.", "assistant:run-1-1", "tool:t1", "assistant:run-2-1"}
	if !slices.Equal(ids(got), want) {
		t.Fatalf("the thread holds %v, want %v", ids(got), want)
	}
	if got[3].Text() != "Done." || got[1].ToolCalls[0].Function.Arguments != `{"field":"email"}` {
		t.Errorf("the stored messages: %+v", got)
	}
}

// forged is the resume body with the call's arguments changed and the thinking
// dropped, as a client could post it.
func forged(t *testing.T) aguix.RunInput {
	in := inbound(t, "reasoning-approve")
	in.Messages = slices.DeleteFunc(slices.Clone(in.Messages), func(m aguix.Message) bool { return m.Role == "reasoning" })
	for i, m := range in.Messages {
		if len(m.ToolCalls) > 0 {
			calls := slices.Clone(m.ToolCalls)
			calls[0].Function.Arguments = `{"book_id":9}`
			in.Messages[i].ToolCalls = calls
		}
	}
	return in
}

func TestAResumeStartsFromTheStoredThread(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store bool
		ran   string
	}{
		{"stored", true, "discard 7"},
		{"no store", false, "discard 9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newArchive(t)
			llm := &scriptLLM{steps: []step{
				{thoughts: thinking(), calls: []*genai.FunctionCall{discardCall("c1", 7)}},
				{text: "Discarded."},
			}}
			approval := NewApproval(a.entries)
			opts := []Option{WithApproval(approval)}
			store := newMemStore()
			if tc.store {
				opts = append(opts, WithThreadStore(store))
			}
			bridge := mustNew(t, librarian(t, llm, approval.Gate(a.ts)), opts...)
			if o := outcome(t, serve(t, bridge, input("run-pause", text("user", "Discard book 7.")))); o["type"] != "interrupt" {
				t.Fatalf("outcome %v", o)
			}
			s := serve(t, bridge, forged(t))
			if ran := a.ran(); !slices.Equal(ran, []string{tc.ran}) {
				t.Fatalf("ran %v, want %s:\n%s", ran, tc.ran, s.body)
			}
			if tc.store {
				wantThinkingBeforeTheCall(t, llm.request(1).Contents, "c1")
				got := ids(store.thread("anonymous", "thread-1"))
				want := []string{"user:user-Discard book 7.", "reasoning:run-pause-1", "assistant:c1",
					"tool:run-reasoning-approve-1", "assistant:run-reasoning-approve-2"}
				if !slices.Equal(got, want) {
					t.Errorf("the thread holds %v, want %v", got, want)
				}
			}
		})
	}
}

func TestAResumeOfAThreadTheStoreLacksUsesTheClientsMessages(t *testing.T) {
	a := newArchive(t)
	llm := &scriptLLM{steps: []step{{text: "Discarded."}}}
	approval := NewApproval(a.entries)
	bridge := mustNew(t, librarian(t, llm, approval.Gate(a.ts)), WithApproval(approval), WithThreadStore(newMemStore()))
	s := serve(t, bridge, forged(t))
	if ran := a.ran(); !slices.Equal(ran, []string{"discard 9"}) {
		t.Fatalf("ran %v:\n%s", ran, s.body)
	}
}

func TestOpenAnswersTakesTheClientsTrailingAnswersToOpenCalls(t *testing.T) {
	call := func(id string) aguix.ToolCall {
		return aguix.ToolCall{ID: id, Type: "function", Function: aguix.FunctionCall{Name: "fill_field"}}
	}
	tool := func(id string) aguix.Message { return aguix.Message{ID: "t-" + id, Role: "tool", ToolCallID: id} }
	stored := []aguix.Message{assistant("", call("c1"), call("c2"), call("c4")), tool("c1")}
	client := []aguix.Message{tool("c4"), text("user", "Hi."), tool("c1"), tool("c2"), tool("c3"), tool("c2")}
	if got := ids(openAnswers(stored, client)); !slices.Equal(got, []string{"tool:t-c2"}) {
		t.Errorf("answers %v, want only c2's, once", got)
	}
	if got := openAnswers(stored, nil); got != nil {
		t.Errorf("no client messages answered %v", got)
	}
}

func TestEachUserHasTheirOwnThread(t *testing.T) {
	store := newMemStore()
	for _, user := range []string{"alice", "bob"} {
		llm := &scriptLLM{steps: []step{{text: "Hello " + user + "."}}}
		bridge := mustNew(t, librarian(t, llm), asUser(user), WithThreadStore(store))
		serve(t, bridge, input("run-"+user, text("user", "Hi, I am "+user+".")))
	}
	for _, user := range []string{"alice", "bob"} {
		got := store.thread(user, "thread-1")
		if len(got) != 2 || got[0].Text() != "Hi, I am "+user+"." || got[1].Text() != "Hello "+user+"." {
			t.Errorf("%s's thread holds %+v", user, got)
		}
	}
}

func TestAThreadIsNotSavedWhenTheRunFailsOrHasNothingToAnswer(t *testing.T) {
	store := newMemStore()
	llm := &scriptLLM{steps: []step{{err: errors.New("model down")}}}
	bridge := mustNew(t, librarian(t, llm), WithThreadStore(store))
	if s := serve(t, bridge, input("run-1", text("user", "Hi."))); s.last()["type"] != "RUN_ERROR" {
		t.Fatalf("the run did not fail:\n%s", s.body)
	}
	serve(t, bridge, input("run-2", text("user", "Hi."), assistant("Hello.")))
	if store.saves != 0 {
		t.Errorf("saved %d times", store.saves)
	}
}

func TestAStoreFailureFailsTheRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(*memStore)
		in     aguix.RunInput
		want   string
	}{
		{"save", func(s *memStore) { s.saveErr = errors.New("disk full") }, input("run-1", text("user", "Hi.")),
			"aguiadk: saving the thread: disk full"},
		{"load", func(s *memStore) { s.loadErr = errors.New("db gone") }, forged(t),
			"aguiadk: loading the thread: db gone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemStore()
			tc.break_(store)
			a := newArchive(t)
			approval := NewApproval(a.entries)
			llm := &scriptLLM{steps: []step{{text: "Hello."}}}
			bridge := mustNew(t, librarian(t, llm, approval.Gate(a.ts)), WithApproval(approval), WithThreadStore(store))
			s := serve(t, bridge, tc.in)
			if s.last()["type"] != "RUN_ERROR" || len(s.errs) != 1 || s.errs[0].Error() != tc.want {
				t.Errorf("errs %v, body:\n%s", s.errs, s.body)
			}
		})
	}
}

func TestASaveOutlivesACancelledRequest(t *testing.T) {
	store := newMemStore()
	b := &bridge{store: store}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.saveThread(ctx, "u", "t", nil, &recorder{}); err != nil || store.saves != 1 || store.saveCtx != nil {
		t.Errorf("err %v, saves %d, the store saw %v", err, store.saves, store.saveCtx)
	}
}

func TestWithThreadStoreRefusesNil(t *testing.T) {
	_, err := New(librarian(t, &scriptLLM{}), WithUserID(Anonymous), WithThreadStore(nil))
	if err == nil || !strings.Contains(err.Error(), "WithThreadStore needs a store") {
		t.Errorf("err %v", err)
	}
}

func TestTheRecorderKeepsAFailedCallsOutcome(t *testing.T) {
	var r recorder
	r.observe(aguix.ToolCallResult("m1", "orphan", "nope", aguix.OutcomeFailed))
	got, _ := json.Marshal(r.messages())
	if want := `[{"id":"m1","role":"tool","content":"nope","toolCallId":"orphan","metadata":{"outcome":"failed"}}]`; string(got) != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}
