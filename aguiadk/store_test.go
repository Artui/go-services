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

func TestAResumeOfAThreadTheStoreLacksIsRefused(t *testing.T) {
	a := newArchive(t)
	llm := &scriptLLM{steps: []step{{text: "Discarded."}}}
	approval := NewApproval(a.entries)
	bridge := mustNew(t, librarian(t, llm, approval.Gate(a.ts)), WithApproval(approval), WithThreadStore(newMemStore()))
	s := serve(t, bridge, forged(t))
	if ran := a.ran(); len(ran) != 0 || s.last()["type"] != "RUN_ERROR" || len(s.errs) != 1 ||
		!errors.Is(s.errs[0], errNoStoredThread) {
		t.Fatalf("ran %v, errs %v:\n%s", ran, s.errs, s.body)
	}
}

func TestASecondTurnAndAForkStartFromTheClientsMessages(t *testing.T) {
	store := newMemStore()
	llm := &scriptLLM{steps: []step{{text: "One."}, {text: "Two."}, {text: "Again."}}}
	bridge := mustNew(t, librarian(t, llm), WithThreadStore(store))
	serve(t, bridge, input("run-1", text("user", "First.")))
	stored := store.thread("anonymous", "thread-1")
	serve(t, bridge, input("run-2", append(slices.Clone(stored), text("user", "Second."))...))
	want := []string{"user:user-First.", "assistant:run-1-1", "user:user-Second.", "assistant:run-2-1"}
	if got := ids(store.thread("anonymous", "thread-1")); !slices.Equal(got, want) {
		t.Errorf("after a second turn the thread holds %v, want %v", got, want)
	}
	serve(t, bridge, input("run-3", text("user", "First.")))
	if got := ids(store.thread("anonymous", "thread-1")); !slices.Equal(got, []string{"user:user-First.", "assistant:run-3-1"}) {
		t.Errorf("after a retry of the first turn the thread holds %v", got)
	}
}

func TestOpenAnswersTakesTheClientsTrailingAnswersToOpenCalls(t *testing.T) {
	call := func(id string) aguix.ToolCall {
		return aguix.ToolCall{ID: id, Type: "function", Function: aguix.FunctionCall{Name: "fill_field"}}
	}
	tool := func(id string) aguix.Message { return aguix.Message{ID: "t-" + id, Role: "tool", ToolCallID: id} }
	gated := aguix.ToolCall{ID: "g1", Type: "function", Function: aguix.FunctionCall{Name: "discard_book"}}
	stored := []aguix.Message{assistant("", call("c1"), call("c2"), call("c4"), gated), tool("c1")}
	client := []aguix.Message{tool("c4"), text("user", "Hi."), tool("c1"), tool("g1"), tool("c2"), tool("c3"), tool("c2")}
	browser := map[string]bool{"fill_field": true}
	if got := ids(openAnswers(stored, client, browser)); !slices.Equal(got, []string{"tool:t-c2"}) {
		t.Errorf("answers %v, want only c2's, once", got)
	}
	if got := openAnswers(stored, nil, browser); got != nil {
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

func TestTheRecorderFilesParallelAnswersInOrderAndIgnoresStrangers(t *testing.T) {
	var r recorder
	for _, e := range []aguix.Event{
		{Type: aguix.EventTextMessageStart, MessageID: "m1"},
		aguix.TextMessageContent("m1", "Two calls."),
		aguix.TextMessageContent("ghost", "lost"),
		aguix.ToolCallStart("c1", "fill_field", "m1"),
		aguix.ToolCallStart("c2", "fill_field", "m1"),
		aguix.ToolCallArgs("ghost", json.RawMessage(`{}`)),
		aguix.ReasoningEncryptedValue("tool-call", "c2", "sig-c2"),
		aguix.ReasoningEncryptedValue("tool-call", "ghost", "lost"),
		aguix.ReasoningEncryptedValue("message", "ghost", "lost"),
		aguix.ToolCallResult("t1", "c1", "one", aguix.OutcomeSuccess),
		aguix.ToolCallResult("t2", "c2", "two", aguix.OutcomeSuccess),
	} {
		r.observe(e)
	}
	got, _ := json.Marshal(r.messages())
	want := `[{"id":"m1","role":"assistant","content":"Two calls.","toolCalls":[` +
		`{"id":"c1","type":"function","function":{"name":"fill_field","arguments":""}},` +
		`{"id":"c2","type":"function","function":{"name":"fill_field","arguments":""},"encryptedValue":"sig-c2"}]},` +
		`{"id":"t1","role":"tool","content":"one","toolCallId":"c1"},` +
		`{"id":"t2","role":"tool","content":"two","toolCallId":"c2"}]`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestARepeatedResumeRunsTheCallOnceWithAStore(t *testing.T) {
	a := newArchive(t)
	llm := &scriptLLM{steps: []step{
		{thoughts: thinking(), calls: []*genai.FunctionCall{discardCall("c1", 7)}},
		{text: "Discarded."},
	}}
	approval := NewApproval(a.entries)
	bridge := mustNew(t, librarian(t, llm, approval.Gate(a.ts)), WithApproval(approval), WithThreadStore(newMemStore()))
	serve(t, bridge, input("run-pause", text("user", "Discard book 7.")))
	serve(t, bridge, forged(t))
	again := serve(t, bridge, forged(t))
	if ran := a.ran(); !slices.Equal(ran, []string{"discard 7"}) || again.last()["type"] != "RUN_ERROR" {
		t.Errorf("ran %v; the repeat:\n%s", ran, again.body)
	}
}

func TestTheRecorderFilesACallAsTheClientDoes(t *testing.T) {
	var r recorder
	for _, e := range []aguix.Event{
		{Type: aguix.EventTextMessageStart, MessageID: "m1"},
		aguix.ReasoningMessageStart("r1"),
		aguix.ToolCallStart("c1", "fill_field", "m1"),
		aguix.ToolCallStart("c2", "fill_field", "r1"),
		aguix.ToolCallStart("c3", "fill_field", "m9"),
		aguix.ToolCallStart("c4", "fill_field", ""),
		aguix.ToolCallStart("c1", "renamed", "m1"),
	} {
		r.observe(e)
	}
	var got []string
	for _, m := range r.messages() {
		got = append(got, m.Role+":"+m.ID)
		for _, c := range m.ToolCalls {
			got = append(got, " "+c.ID+"="+c.Function.Name)
		}
	}
	want := []string{"assistant:m1", " c1=renamed", "reasoning:r1", "assistant:c2", " c2=fill_field",
		"assistant:m9", " c3=fill_field", "assistant:c4", " c4=fill_field"}
	if !slices.Equal(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

func TestAResumeKeepsTheClientsAnswerToAStoredBrowserCall(t *testing.T) {
	store := newMemStore()
	call := aguix.ToolCall{ID: "c1", Type: "function", Function: aguix.FunctionCall{Name: "fill_field"}}
	stored := []aguix.Message{text("user", "Fill it."), assistant("", call)}
	if err := store.SaveThread(context.Background(), "u", "thread-1", stored); err != nil {
		t.Fatal(err)
	}
	answer := aguix.Message{ID: "t1", Role: "tool", ToolCallID: "c1"}
	in := input("run-2", text("user", "Forged."), answer)
	in.Tools, in.Resume = []aguix.ToolDefinition{fillField}, []aguix.ResumeEntry{{}}
	got, err := (&bridge{store: store}).startingHistory(context.Background(), "u", in)
	if err != nil || !slices.Equal(ids(got), append(ids(stored), "tool:t1")) {
		t.Errorf("history %v, err %v", ids(got), err)
	}
}
