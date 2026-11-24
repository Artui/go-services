package aguiadk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"slices"
	"sync"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagents/parallelagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

// shelf is a registry whose one mutation publishes an invalidation from inside
// its transaction; log records the order of commits and publications.
type shelf struct {
	mu  sync.Mutex
	log []string
}

func (s *shelf) record(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, line)
}

func (s *shelf) toolset(t *testing.T) tool.Toolset {
	t.Helper()
	reg := services.New[struct{}](nil, services.WithAtomic[struct{}](
		func(ctx context.Context, run func(context.Context) error) error {
			err := run(ctx)
			s.record("commit")
			return err
		}))
	services.MustRegister(reg, services.Spec[struct{}, borrowIn, borrowOut]{
		Name: "shelve_book", Description: "Shelve a book.", Kind: services.Mutation,
		Run: func(c services.Ctx[struct{}], in borrowIn) (borrowOut, error) {
			reason := "shelve_book"
			if in.BookID == 0 {
				reason = ""
			}
			listening := PublishInvalidation(c.Context, reason, "books", fmt.Sprintf("books/%d", in.BookID))
			s.record(fmt.Sprintf("publish %d %v", in.BookID, listening))
			return borrowOut{Loan: in.BookID}, nil
		},
	})
	ts, err := adkx.Toolset(reg, adkx.Anonymous)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func shelveBook(id string, book int) *genai.FunctionCall {
	return &genai.FunctionCall{ID: id, Name: "shelve_book", Args: map[string]any{"book_id": book}}
}

func customs(s served) []string {
	var out []string
	for _, f := range s.frames {
		if f["type"] == "CUSTOM" {
			raw, _ := json.Marshal(f)
			out = append(out, string(raw))
		}
	}
	return out
}

func TestAMutationsInvalidationFollowsItsResult(t *testing.T) {
	sh := &shelf{}
	llm := &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{shelveBook("c1", 7)}}, {text: "Shelved."}}}
	s := serve(t, mustNew(t, librarian(t, llm, sh.toolset(t))), input("run-inv", text("user", "Shelve book 7.")))

	wantTypes(t, s, "RUN_STARTED", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END",
		"TOOL_CALL_RESULT", "CUSTOM",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END", "RUN_FINISHED")
	want := []string{`{"name":"ag_ui.invalidate","type":"CUSTOM","value":{"keys":["books","books/7"],"reason":"shelve_book"}}`}
	if got := customs(s); !slices.Equal(got, want) {
		t.Errorf("custom frames:\n got %v\nwant %v", got, want)
	}
	// Published inside the transaction, sent once the result shows it committed.
	if !slices.Equal(sh.log, []string{"publish 7 true", "commit"}) {
		t.Errorf("log %v", sh.log)
	}
	golden(t, "invalidate", s)
}

// A step's calls may run concurrently; each one's invalidation is sent, after
// the event that carries the step's results.
func TestEveryCallOfAStepIsInvalidated(t *testing.T) {
	sh := &shelf{}
	llm := &scriptLLM{steps: []step{
		{calls: []*genai.FunctionCall{shelveBook("c1", 7), shelveBook("c2", 0)}}, {text: "Shelved."},
	}}
	s := serve(t, mustNew(t, librarian(t, llm, sh.toolset(t))), input("run-inv", text("user", "Shelve both.")))

	got := customs(s)
	slices.Sort(got)
	want := []string{
		`{"name":"ag_ui.invalidate","type":"CUSTOM","value":{"keys":["books","books/0"],"reason":null}}`,
		`{"name":"ag_ui.invalidate","type":"CUSTOM","value":{"keys":["books","books/7"],"reason":"shelve_book"}}`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("custom frames:\n got %v\nwant %v", got, want)
	}
	last := -1
	for i, f := range s.frames {
		if f["type"] == "TOOL_CALL_RESULT" {
			last = i
		}
	}
	if first := slices.IndexFunc(s.frames, func(f map[string]any) bool { return f["type"] == "CUSTOM" }); first < last {
		t.Errorf("an invalidation preceded the results:\n%s", s.body)
	}
}

// What was published before the run failed is still sent, ahead of RUN_ERROR.
func TestAnInvalidationQueuedBeforeAFailureIsSent(t *testing.T) {
	boom := errors.New("callback refused")
	a, err := llmagent.New(llmagent.Config{
		Name: "librarian", Model: &scriptLLM{},
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{
			func(ctx agent.Context, _ *model.LLMRequest) (*model.LLMResponse, error) {
				PublishInvalidation(ctx, "", "books")
				return nil, boom
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := serve(t, mustNew(t, a), input("run-inv", text("user", "Shelve.")))
	wantTypes(t, s, "RUN_STARTED", "CUSTOM", "RUN_ERROR")
	wantRunError(t, s, boom.Error())
}

func TestPublishInvalidationOutsideARunOrWithoutKeysQueuesNothing(t *testing.T) {
	if PublishInvalidation(context.Background(), "why", "books") {
		t.Error("a context with no run reported a listener")
	}
	ctx, queued := withSink(context.Background())
	if PublishInvalidation(ctx, "why") {
		t.Error("an invalidation with no keys reported a listener")
	}
	keys := []string{"books"}
	if !PublishInvalidation(ctx, "why", keys...) {
		t.Error("a run's context reported no listener")
	}
	keys[0] = "changed"
	got := joined(queued.drain(nil))
	if len(got) != 1 || got[0].Type != aguix.EventCustom || got[0].Name != InvalidateEventName {
		t.Fatalf("queued %v", got)
	}
	raw, _ := json.Marshal(got[0])
	if string(raw) != `{"type":"CUSTOM","name":"ag_ui.invalidate","value":{"keys":["books"],"reason":"why"}}` {
		t.Errorf("queued %s", raw)
	}
	if again := joined(queued.drain(nil)); len(again) != 0 {
		t.Errorf("a drained sink still held %v", again)
	}
}

// gateLLM answers once with text, but only after open closes, and closes
// emitted once the bridge has taken its last response.
type gateLLM struct {
	text          string
	open, emitted chan struct{}
}

func (m *gateLLM) Name() string { return "gate" }

func (m *gateLLM) GenerateContent(
	ctx context.Context, _ *model.LLMRequest, _ bool,
) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		select {
		case <-m.open:
		case <-ctx.Done():
			yield(nil, ctx.Err())
			return
		}
		yield(&model.LLMResponse{
			Content: genai.NewContentFromText(m.text, genai.RoleModel), TurnComplete: true,
		}, nil)
		close(m.emitted)
	}
}

type txKey struct{}

// A sibling branch's event, sent while a call that published is still running,
// must not carry that call's invalidation ahead of the call's own result; the
// spec's transaction wraps its context, as a real one does.
func TestUnderAParallelAgentAnInvalidationFollowsItsOwnCallsResult(t *testing.T) {
	published := make(chan struct{})
	chatter := &gateLLM{text: "Meanwhile.", open: published, emitted: make(chan struct{})}
	reg := services.New[struct{}](nil, services.WithAtomic[struct{}](
		func(ctx context.Context, run func(context.Context) error) error {
			return run(context.WithValue(ctx, txKey{}, "tx"))
		}))
	services.MustRegister(reg, services.Spec[struct{}, borrowIn, borrowOut]{
		Name: "shelve_book", Description: "Shelve a book.", Kind: services.Mutation,
		Run: func(c services.Ctx[struct{}], in borrowIn) (borrowOut, error) {
			PublishInvalidation(c.Context, "shelve_book", fmt.Sprintf("books/%d", in.BookID))
			close(published)
			select {
			case <-chatter.emitted:
			case <-c.Context.Done():
			}
			return borrowOut{Loan: in.BookID}, nil
		},
	})
	ts, err := adkx.Toolset(reg, adkx.Anonymous)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := llmagent.New(llmagent.Config{
		Name: "writer", Toolsets: []tool.Toolset{ts},
		Model: &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{shelveBook("c1", 7)}}, {text: "Shelved."}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	talker, err := llmagent.New(llmagent.Config{Name: "talker", Model: chatter})
	if err != nil {
		t.Fatal(err)
	}
	root, err := parallelagent.New(parallelagent.Config{AgentConfig: agent.Config{
		Name: "both", SubAgents: []agent.Agent{writer, talker},
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := serve(t, mustNew(t, root), input("run-par", text("user", "Shelve book 7.")))

	result := slices.IndexFunc(s.frames, func(f map[string]any) bool {
		return f["type"] == "TOOL_CALL_RESULT" && f["toolCallId"] == "c1"
	})
	custom := slices.IndexFunc(s.frames, func(f map[string]any) bool { return f["type"] == "CUSTOM" })
	if result < 0 || custom < 0 || custom < result {
		t.Fatalf("the invalidation (frame %d) preceded its call's result (frame %d):\n%s", custom, result, s.body)
	}
	if meanwhile := slices.IndexFunc(s.frames, func(f map[string]any) bool {
		return f["type"] == "TEXT_MESSAGE_CONTENT" && f["delta"] == "Meanwhile."
	}); meanwhile < 0 || meanwhile > result {
		t.Fatalf("the sibling's text (frame %d) did not arrive while the call ran (result %d):\n%s",
			meanwhile, result, s.body)
	}
	golden(t, "invalidate-parallel", s)
}

// ADK runs a streaming tool without the tool callbacks, so the plugin never
// names its call; what it publishes still follows the result it yields.
func TestAStreamingToolsInvalidationFollowsItsResult(t *testing.T) {
	stream, err := functiontool.NewStreaming(functiontool.Config{Name: "count", Description: "Counts."},
		func(ctx agent.Context, _ struct{}) iter.Seq2[string, error] {
			return func(yield func(string, error) bool) {
				PublishInvalidation(ctx, "count", "counts")
				yield("one", nil)
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	a, err := llmagent.New(llmagent.Config{
		Name: "counter", Tools: []tool.Tool{stream},
		Model: &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{{ID: "c1", Name: "count"}}}, {text: "Counted."}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := serve(t, mustNew(t, a), input("run-stream", text("user", "Count.")))
	result, custom := -1, -1
	for i, f := range s.frames {
		switch {
		case f["type"] == "TOOL_CALL_RESULT" && result < 0:
			result = i
		case f["type"] == "CUSTOM":
			custom = i
		}
	}
	if result < 0 || custom < result {
		t.Errorf("the invalidation (%d) is not after the call's result (%d)", custom, result)
	}
}
