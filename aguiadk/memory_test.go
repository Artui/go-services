package aguiadk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/loadmemorytool"
	"google.golang.org/genai"
)

// recordingMemory is ADK's in-memory service, noting each session added.
type recordingMemory struct {
	memory.Service
	mu    sync.Mutex
	added []string
	fail  error
}

func newRecordingMemory() *recordingMemory {
	return &recordingMemory{Service: memory.InMemoryService()}
}

func (r *recordingMemory) AddSessionToMemory(ctx context.Context, s session.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	var texts []string
	for ev := range s.Events().All() {
		if ev.Content != nil {
			for _, p := range ev.Content.Parts {
				if p.Text != "" {
					texts = append(texts, p.Text)
				}
			}
		}
	}
	r.added = append(r.added, s.AppName()+"/"+s.UserID()+"/"+s.ID()+": "+strings.Join(texts, " | "))
	return r.Service.AddSessionToMemory(ctx, s)
}

func (r *recordingMemory) adds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.added)
}

func signedIn(user string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
		})
	}
}

func fromContext(ctx context.Context) (string, error) {
	user, _ := ctx.Value(userKey{}).(string)
	return user, nil
}

func rememberer(t *testing.T, llm *scriptLLM, toolsets ...tool.Toolset) agent.Agent {
	t.Helper()
	a, err := llmagent.New(llmagent.Config{
		Name: "librarian", Model: llm, Tools: []tool.Tool{loadmemorytool.New()}, Toolsets: toolsets,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func loadMemoryCall(id string) *genai.FunctionCall {
	return &genai.FunctionCall{ID: id, Name: "load_memory", Args: map[string]any{"query": "locker code"}}
}

// recalled is what the load_memory call answered, as the model was sent it.
func recalled(t *testing.T, req int, llm *scriptLLM) string {
	t.Helper()
	for _, c := range llm.request(req).Contents {
		for _, p := range c.Parts {
			if p.FunctionResponse != nil && p.FunctionResponse.Name == "load_memory" {
				raw, err := json.Marshal(p.FunctionResponse.Response)
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			}
		}
	}
	t.Fatalf("request %d carries no load_memory answer", req)
	return ""
}

func TestWithMemoryRecallsOnlyTheRunsUsersThreads(t *testing.T) {
	llm := &scriptLLM{steps: []step{
		{text: "Noted."},
		{calls: []*genai.FunctionCall{loadMemoryCall("m1")}}, {text: "I do not know it."},
		{calls: []*genai.FunctionCall{loadMemoryCall("m2")}}, {text: "It is 4417."},
	}}
	bridge := mustNew(t, rememberer(t, llm), WithUserID(fromContext), WithMemory(memory.InMemoryService()))
	tell := input("run-1", text("user", "My locker code is 4417."))
	tell.ThreadID = "ada-1"
	serve(t, bridge, tell, signedIn("ada"))

	ask := input("run-2", text("user", "What is my locker code?"))
	ask.ThreadID = "bob-1"
	if s := serve(t, bridge, ask, signedIn("bob")); s.last()["type"] != "RUN_FINISHED" {
		t.Fatalf("bob's run failed:\n%s", s.body)
	}
	if got := recalled(t, 2, llm); strings.Contains(got, "4417") {
		t.Errorf("bob recalled ada's thread: %s", got)
	}

	ask.ThreadID, ask.RunID = "ada-2", "run-3"
	serve(t, bridge, ask, signedIn("ada"))
	if got := recalled(t, 4, llm); !strings.Contains(got, "My locker code is 4417.") {
		t.Errorf("ada did not recall her own thread: %s", got)
	}
}

func TestAFinishedRunIsAddedToMemoryOnce(t *testing.T) {
	rec := newRecordingMemory()
	llm := &scriptLLM{steps: []step{
		{text: "Filling it in.", calls: []*genai.FunctionCall{fillCall("c1")}},
		{text: "Done."},
	}}
	bridge := mustNew(t, rememberer(t, llm, ClientToolset()), asUser("ada"), WithMemory(rec))
	first := input("run-1", text("user", "Fill in my email."))
	first.Tools = []aguix.ToolDefinition{fillField}
	if o := outcome(t, serve(t, bridge, first)); o["pendingToolCallIds"] == nil {
		t.Fatalf("the first run left nothing pending: %v", o)
	}
	if got := rec.adds(); len(got) != 0 {
		t.Fatalf("a run waiting on the browser was added: %q", got)
	}

	answer := aguix.Message{ID: "t1", Role: "tool", ToolCallID: "c1", Content: aguix.TextContent(`{"ok":true}`)}
	call := aguix.Message{ID: "a1", Role: "assistant", Content: aguix.TextContent("Filling it in."),
		ToolCalls: []aguix.ToolCall{{ID: "c1", Type: "function", Function: aguix.FunctionCall{Name: "fill_field", Arguments: `{"field":"email"}`}}}}
	second := input("run-2", text("user", "Fill in my email."), call, answer)
	second.Tools = first.Tools
	if s := serve(t, bridge, second); s.last()["type"] != "RUN_FINISHED" {
		t.Fatalf("the answering run failed:\n%s", s.body)
	}
	want := []string{"aguiadk/ada/thread-1: Fill in my email. | Filling it in. | Done."}
	if got := rec.adds(); !slices.Equal(got, want) {
		t.Errorf("added %q, want %q", got, want)
	}
}

func TestARunThatDoesNotFinishIsNotAddedToMemory(t *testing.T) {
	cases := map[string]func(rec *recordingMemory) served{
		"failed": func(rec *recordingMemory) served {
			llm := &scriptLLM{steps: []step{{text: "Half", err: errors.New("provider down")}}}
			return serve(t, mustNew(t, rememberer(t, llm), WithMemory(rec)), input("run-1", text("user", "Hi.")))
		},
		"nothing to answer": func(rec *recordingMemory) served {
			return serve(t, mustNew(t, rememberer(t, &scriptLLM{}), WithMemory(rec)),
				input("run-1", text("user", "Hi."), text("assistant", "Hello.")))
		},
		"interrupted": func(rec *recordingMemory) served {
			a := newArchive(t)
			approval := NewApproval(a.entries)
			llm := &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{discardCall("c1", 7)}}}}
			bridge := mustNew(t, librarian(t, llm, approval.Gate(a.ts)), WithApproval(approval), WithMemory(rec))
			return serve(t, bridge, input("run-1", text("user", "Discard book 7.")))
		},
	}
	for name, run := range cases {
		rec := newRecordingMemory()
		run(rec)
		if got := rec.adds(); len(got) != 0 {
			t.Errorf("%s: added %q", name, got)
		}
	}
}

func TestAMemoryThatFailsFailsTheRun(t *testing.T) {
	rec := newRecordingMemory()
	rec.fail = errors.New("memory unavailable at 10.0.0.4")
	s := serve(t, mustNew(t, rememberer(t, &scriptLLM{}), WithMemory(rec)), input("run-1", text("user", "Hi.")))
	wantRunError(t, s, "adding the run to memory: memory unavailable")

	bridge := mustNew(t, rememberer(t, &scriptLLM{}), WithMemory(newRecordingMemory())).(*bridge)
	inner := bridge.newService
	bridge.newService = func() session.Service { return &lostSession{Service: inner()} }
	wantRunError(t, serve(t, bridge, input("run-1", text("user", "Hi."))), "reading the run's session for memory")
}

// lostSession is a session service that reads a session back for the runner
// alone, then loses it.
type lostSession struct {
	session.Service
	reads int
}

func (l *lostSession) Get(ctx context.Context, req *session.GetRequest) (*session.GetResponse, error) {
	if l.reads++; l.reads > 1 {
		return nil, errors.New("session lost")
	}
	return l.Service.Get(ctx, req)
}

func TestWithMemoryRefusesNil(t *testing.T) {
	if _, err := New(rememberer(t, &scriptLLM{}), asUser("ada"), WithMemory(nil)); err == nil ||
		!strings.Contains(err.Error(), "WithMemory needs a memory service") {
		t.Errorf("New took a nil memory service: %v", err)
	}
}
