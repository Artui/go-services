package aguiadk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Artui/go-services/adkx"
	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

func TestServerToolWinsANameCollisionInEitherOrder(t *testing.T) {
	clash := aguix.ToolDefinition{Name: "borrow_book", Description: "The page's own borrow.",
		Parameters: json.RawMessage(`{"type":"object"}`)}
	for name, server := range map[string]bool{"server first": true, "client first": false} {
		t.Run(name, func(t *testing.T) {
			lib := &library{}
			llm := &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{borrowCall("c1", 2)}}}}
			toolsets := []tool.Toolset{lib.toolset(t, adkx.Anonymous), ClientToolset()}
			if !server {
				slices.Reverse(toolsets)
			}
			in := input("run-1", text("user", "Borrow book 2."))
			in.Tools = []aguix.ToolDefinition{clash, fillField}
			s := serve(t, mustNew(t, librarian(t, llm, toolsets...)), in)

			declared := declarations(llm.request(0))
			if declared["borrow_book"].Description != "Lend a book." || declared["borrow_book (again)"] != nil {
				t.Errorf("declared %v", declared)
			}
			if declared["fill_field"] == nil {
				t.Errorf("the other client tool was dropped too")
			}
			if got := lib.ran(); len(got) != 1 {
				t.Errorf("the server tool ran %v", got)
			}
			if _, has := s.last()["outcome"]; has || s.last()["type"] != "RUN_FINISHED" {
				t.Errorf("last frame %v", s.last())
			}
		})
	}
}

func TestDuplicateClientToolsDeclareTheFirst(t *testing.T) {
	llm := &scriptLLM{}
	in := input("run-1", text("user", "Hi."))
	in.Tools = []aguix.ToolDefinition{
		{Name: "confirm", Description: "first", Parameters: json.RawMessage(" null ")},
		{Name: "confirm", Description: "second"},
	}
	serve(t, mustNew(t, librarian(t, llm, ClientToolset())), in)
	declared := declarations(llm.request(0))
	if d := declared["confirm"]; d == nil || d.Description != "first" || d.ParametersJsonSchema != nil ||
		declared["confirm (again)"] != nil {
		t.Errorf("declared %v", declared)
	}
}

func TestClientToolsetIsEmptyOutsideABridgeRun(t *testing.T) {
	llm := &scriptLLM{}
	a := librarian(t, llm, ClientToolset())
	svc := session.InMemoryService()
	r, err := runner.New(runner.Config{AppName: "app", Agent: a, SessionService: svc})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(t.Context(), &session.CreateRequest{AppName: "app", UserID: "u", SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	for _, err := range r.Run(t.Context(), "u", "s", genai.NewContentFromText("hi", genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if req := llm.request(0); req.Config != nil && len(req.Config.Tools) > 0 {
		t.Errorf("declared %v outside a run", declarations(req))
	}
}

func TestConversationEndingOnTheAssistantFinishesWithoutTheModel(t *testing.T) {
	llm := &scriptLLM{}
	s := serve(t, mustNew(t, librarian(t, llm)),
		input("run-1", text("user", "Hi."), text("assistant", "Hello.")))
	wantTypes(t, s, "RUN_STARTED", "RUN_FINISHED")
	if llm.calls() != 0 {
		t.Errorf("the model was called %d times", llm.calls())
	}
}

type userKey struct{}

func TestWithUserIDNamesTheRequestsUser(t *testing.T) {
	lib := &library{}
	llm := &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{borrowCall("c1", 4)}}}}
	bridge := mustNew(t, librarian(t, llm, lib.toolset(t, adkx.UserID)),
		WithUserID(func(ctx context.Context) (string, error) {
			user, _ := ctx.Value(userKey{}).(string)
			return user, nil
		}))
	signIn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, "ada")))
		})
	}
	serve(t, bridge, input("run-1", text("user", "Borrow book 4.")), signIn)
	if got := lib.ran(); !slices.Equal(got, []string{`book 4 for "ada"`}) {
		t.Errorf("the spec ran %v", got)
	}

	// Without the middleware the function finds no user, and the run fails.
	s := serve(t, bridge, input("run-2", text("user", "Borrow book 4.")))
	wantRunError(t, s, "names no user")
}

// wantRunError asserts the client saw aguix's redacted text, and the server's
// observer the real error.
func wantRunError(t *testing.T, s served, observed string) {
	t.Helper()
	if s.last()["type"] != "RUN_ERROR" || s.last()["message"] != aguix.RunErrorText {
		t.Errorf("last frame %v", s.last())
	}
	if len(s.errs) != 1 || !strings.Contains(s.errs[0].Error(), observed) {
		t.Errorf("observed %v, want one error containing %q", s.errs, observed)
	}
}

// brokenService fails the session call the test names.
type brokenService struct {
	session.Service
	create, append error
}

func (b brokenService) Create(ctx context.Context, req *session.CreateRequest) (*session.CreateResponse, error) {
	if b.create != nil {
		return nil, b.create
	}
	return b.Service.Create(ctx, req)
}

func (b brokenService) AppendEvent(ctx context.Context, s session.Session, ev *session.Event) error {
	if b.append != nil {
		return b.append
	}
	return b.Service.AppendEvent(ctx, s, ev)
}

func TestEveryFailureBeforeTheAgentRunsIsARedactedRunError(t *testing.T) {
	failing := errors.New("store unavailable at 10.0.0.4")
	history := input("run-1", text("user", "Hi."), text("assistant", "Hello."), text("user", "Again."))
	badSchema := input("run-1", text("user", "Hi."))
	badSchema.Tools = []aguix.ToolDefinition{{Name: "fill_field", Parameters: json.RawMessage(`[1]`)}}
	for name, tc := range map[string]struct {
		in       aguix.RunInput
		opts     []Option
		alter    func(*bridge)
		observed string
	}{
		"rebuild": {
			in:       input("run-1", aguix.Message{ID: "t1", Role: "tool", ToolCallID: "nope"}),
			observed: "unknown tool call",
		},
		"client tool schema": {in: badSchema, observed: "not a JSON object"},
		"user id": {
			in: history,
			opts: []Option{WithUserID(func(context.Context) (string, error) {
				return "", failing
			})},
			observed: "the run's user: store unavailable",
		},
		"session create": {
			in:       history,
			alter:    func(b *bridge) { b.newService = func() session.Service { return brokenService{create: failing} } },
			observed: "creating the run's session",
		},
		"session append": {
			in: history,
			alter: func(b *bridge) {
				b.newService = func() session.Service {
					return brokenService{Service: session.InMemoryService(), append: failing}
				}
			},
			observed: "replaying the conversation",
		},
		"runner": {
			in: history,
			alter: func(b *bridge) {
				b.newRunner = func(session.Service) (*runner.Runner, error) { return nil, failing }
			},
			observed: "store unavailable",
		},
	} {
		t.Run(name, func(t *testing.T) {
			llm := &scriptLLM{}
			a := mustNew(t, librarian(t, llm, ClientToolset()), tc.opts...)
			if tc.alter != nil {
				tc.alter(a.(*bridge))
			}
			s := serve(t, a, tc.in)
			wantTypes(t, s, "RUN_STARTED", "RUN_ERROR")
			wantRunError(t, s, tc.observed)
			if llm.calls() != 0 {
				t.Errorf("the model was called")
			}
		})
	}
}

func TestModelFailureMidStreamClosesTheTextFirst(t *testing.T) {
	llm := &scriptLLM{steps: []step{{text: "Partly there", err: errors.New("provider connection reset")}}}
	s := serve(t, mustNew(t, librarian(t, llm)), input("run-1", text("user", "Hi.")))
	wantTypes(t, s, "RUN_STARTED",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END",
		"RUN_ERROR")
	wantRunError(t, s, "provider connection reset")
	golden(t, "model-error-mid-stream", s)
}

func TestModelErrorCodeIsARedactedRunError(t *testing.T) {
	llm := &scriptLLM{steps: []step{{code: "overloaded"}}}
	s := serve(t, mustNew(t, librarian(t, llm)), input("run-1", text("user", "Hi.")))
	wantTypes(t, s, "RUN_STARTED", "RUN_ERROR")
	wantRunError(t, s, `code "overloaded"`)
	if !errors.Is(s.errs[0], errModelReported) || strings.Contains(s.errs[0].Error(), "provider detail") {
		t.Errorf("observed %v", s.errs[0])
	}
}

var errHungUp = errors.New("client hung up")

// hangingUp is a response writer that takes n frames and then fails.
type hangingUp struct {
	header http.Header
	n      int
}

func (w *hangingUp) Header() http.Header { return w.header }
func (w *hangingUp) WriteHeader(int)     {}
func (w *hangingUp) Flush()              {}
func (w *hangingUp) Write(p []byte) (int, error) {
	if w.n == 0 {
		return 0, errHungUp
	}
	w.n--
	return len(p), nil
}

func runDirect(t *testing.T, a aguix.Agent, frames int, in aguix.RunInput) error {
	t.Helper()
	out, err := aguix.NewStream(&hangingUp{header: http.Header{}, n: frames})
	if err != nil {
		t.Fatal(err)
	}
	return a.Run(t.Context(), in, out)
}

func TestAClientThatHangsUpEndsTheRun(t *testing.T) {
	llm := &scriptLLM{steps: []step{{text: "Hello there"}}}
	err := runDirect(t, mustNew(t, librarian(t, llm)), 1, input("run-1", text("user", "Hi.")))
	if !errors.Is(err, errHungUp) {
		t.Errorf("err = %v", err)
	}
}

func TestStoppingEarlyLeavesNoGoroutineBehind(t *testing.T) {
	before := runtime.NumGoroutine()
	for range 20 {
		lib := &library{}
		llm := &scriptLLM{steps: []step{{text: "Both.", calls: []*genai.FunctionCall{borrowCall("c1", 3), fillCall("c2")}}}}
		in := input("run-1", text("user", "Both."))
		in.Tools = []aguix.ToolDefinition{fillField}
		rec := httptest.NewRecorder()
		out, err := aguix.NewStream(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := mustNew(t, librarian(t, llm, lib.toolset(t, adkx.Anonymous), ClientToolset())).Run(t.Context(), in, out); err != nil {
			t.Fatal(err)
		}
		if llm.calls() != 1 {
			t.Fatalf("model called %d times", llm.calls())
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("%d goroutines before twenty stopped runs, %d after", before, after)
	}
}

func TestNewRefusesNoAgentAndAMalformedTree(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Error("New(nil) succeeded")
	}
	child := func() agent.Agent {
		a, err := llmagent.New(llmagent.Config{Name: "twin", Model: &scriptLLM{}})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	root, err := llmagent.New(llmagent.Config{
		Name: "root", Model: &scriptLLM{}, SubAgents: []agent.Agent{child(), child()},
	})
	if err != nil {
		t.Fatalf("llmagent refuses the tree itself, so this no longer tests New: %v", err)
	}
	if _, err := New(root); err == nil || !strings.Contains(err.Error(), "aguiadk:") {
		t.Errorf("New over a tree with two agents named alike: %v", err)
	}
}

func TestClientToolsDescribeThemselves(t *testing.T) {
	tools, err := clientTools([]aguix.ToolDefinition{fillField})
	if err != nil {
		t.Fatal(err)
	}
	if ClientToolset().Name() != "client" || tools[0].Description() != fillField.Description ||
		!tools[0].IsLongRunning() {
		t.Errorf("toolset %q, tool %q", ClientToolset().Name(), tools[0].Description())
	}
}

func TestConcurrentRunsOfOneThreadShareNothing(t *testing.T) {
	bridge := mustNew(t, librarian(t, &scriptLLM{}, ClientToolset()))
	for i := range 8 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			s := serve(t, bridge, input(fmt.Sprintf("run-%d", i), text("user", "Hi.")))
			if s.last()["type"] != "RUN_FINISHED" {
				t.Errorf("last frame %v", s.last())
			}
		})
	}
}
