package aguiadk

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

var update = flag.Bool("update", false, "rewrite the SSE transcripts under testdata")

// step is one scripted model answer. A step with err fails mid-stream, after its
// text partials; a step with code answers with that error code; a step that
// blocks waits for its context to end. usage and model ride on every response.
type step struct {
	text  string
	calls []*genai.FunctionCall
	err   error
	code  string
	block bool
	usage *genai.GenerateContentResponseUsageMetadata
	model string
}

// scriptLLM answers as anthropicmodel does under SSE: the text as partials,
// then one complete response repeating it with the calls. It records every
// request it is sent.
type scriptLLM struct {
	mu       sync.Mutex
	steps    []step
	requests []*model.LLMRequest
}

func (m *scriptLLM) Name() string { return "script" }

func (m *scriptLLM) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *scriptLLM) request(i int) *model.LLMRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests[i]
}

func (m *scriptLLM) GenerateContent(
	ctx context.Context, req *model.LLMRequest, stream bool,
) iter.Seq2[*model.LLMResponse, error] {
	// As a provider does, a cancelled context sends no request.
	if err := ctx.Err(); err != nil {
		return func(yield func(*model.LLMResponse, error) bool) { yield(nil, err) }
	}
	m.mu.Lock()
	s := step{text: "Done."}
	if n := len(m.requests); n < len(m.steps) {
		s = m.steps[n]
	}
	m.requests = append(m.requests, req)
	m.mu.Unlock()
	return func(yield func(*model.LLMResponse, error) bool) {
		if s.block {
			<-ctx.Done()
			yield(nil, ctx.Err())
			return
		}
		if s.code != "" {
			yield(&model.LLMResponse{ErrorCode: s.code, ErrorMessage: "provider detail"}, nil)
			return
		}
		if stream && s.text != "" {
			for _, word := range strings.SplitAfter(s.text, " ") {
				if !yield(&model.LLMResponse{
					Content: genai.NewContentFromText(word, genai.RoleModel), Partial: true,
					UsageMetadata: s.usage, ModelVersion: s.model,
				}, nil) {
					return
				}
			}
		}
		if s.err != nil {
			yield(nil, s.err)
			return
		}
		var parts []*genai.Part
		if s.text != "" {
			parts = append(parts, genai.NewPartFromText(s.text))
		}
		for _, c := range s.calls {
			parts = append(parts, &genai.Part{FunctionCall: c})
		}
		yield(&model.LLMResponse{
			Content: &genai.Content{Role: genai.RoleModel, Parts: parts}, TurnComplete: true,
			UsageMetadata: s.usage, ModelVersion: s.model,
		}, nil)
	}
}

type borrowIn struct {
	BookID int `json:"book_id"`
}

type borrowOut struct {
	Loan  int    `json:"loan"`
	By    string `json:"by"`
	Error string `json:"error,omitempty"`
}

// library is a registry with one spec that records each call it runs.
type library struct {
	mu   sync.Mutex
	runs []string
}

func (l *library) ran() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.runs)
}

func (l *library) toolset(t *testing.T, principal adkx.Principal) tool.Toolset {
	t.Helper()
	reg := services.New(func(_ context.Context, who any) (string, error) {
		name, _ := who.(string)
		return name, nil
	})
	services.MustRegister(reg, services.Spec[string, borrowIn, borrowOut]{
		Name: "borrow_book", Description: "Lend a book.", Kind: services.Mutation,
		Run: func(c services.Ctx[string], in borrowIn) (borrowOut, error) {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.runs = append(l.runs, fmt.Sprintf("book %d for %q", in.BookID, c.Deps))
			return borrowOut{Loan: in.BookID * 10, By: c.Deps}, nil
		},
	})
	ts, err := adkx.Toolset(reg, principal)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func librarian(t *testing.T, llm model.LLM, toolsets ...tool.Toolset) agent.Agent {
	t.Helper()
	a, err := llmagent.New(llmagent.Config{Name: "librarian", Model: llm, Toolsets: toolsets})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustNew(t *testing.T, root agent.Agent, opts ...Option) aguix.Agent {
	t.Helper()
	b, err := New(root, append([]Option{WithUserID(Anonymous)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// served is one run as the client saw it.
type served struct {
	body   string
	frames []map[string]any
	errs   []error
}

func (s served) types() []string {
	var out []string
	for _, f := range s.frames {
		out = append(out, f["type"].(string))
	}
	return out
}

func (s served) first(typ string) map[string]any {
	for _, f := range s.frames {
		if f["type"] == typ {
			return f
		}
	}
	return nil
}

func (s served) last() map[string]any { return s.frames[len(s.frames)-1] }

// serve posts in to the bridge through aguix.Handler, with wrap around it as a
// server's middleware would be.
func serve(t *testing.T, a aguix.Agent, in aguix.RunInput, wrap ...func(http.Handler) http.Handler) served {
	t.Helper()
	return serveWith(t, a, in, nil, wrap...)
}

// serveWith is serve with options for the handler.
func serveWith(
	t *testing.T, a aguix.Agent, in aguix.RunInput, opts []aguix.Option, wrap ...func(http.Handler) http.Handler,
) served {
	t.Helper()
	var (
		mu   sync.Mutex
		errs []error
	)
	h, err := aguix.Handler(a, append(opts, aguix.WithOnError(func(_ *http.Request, err error) {
		mu.Lock()
		defer mu.Unlock()
		errs = append(errs, err)
	}))...)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range wrap {
		h = w(h)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	payload, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body bytes.Buffer
	if _, err := body.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	out := served{body: body.String()}
	for _, chunk := range strings.Split(strings.TrimSuffix(out.body, "\n\n"), "\n\n") {
		var frame map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(chunk, "data: ")), &frame); err != nil {
			t.Fatalf("frame %q: %v", chunk, err)
		}
		out.frames = append(out.frames, frame)
	}
	mu.Lock()
	defer mu.Unlock()
	out.errs = errs
	return out
}

// golden compares a run's SSE body with its transcript, which the wire check
// in scripts/agui-wire reads. The body's final blank line is not stored: the
// repository's end-of-file hook keeps exactly one trailing newline.
func golden(t *testing.T, name string, s served) {
	t.Helper()
	path := filepath.Join("testdata", "agui", "transcripts", name+".sse")
	got := []byte(strings.TrimSuffix(s.body, "\n"))
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the tests with -update to write it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s drifted from the transcript:\ngot:\n%s\nwant:\n%s", name, got, want)
	}
}

func text(role, content string) aguix.Message {
	return aguix.Message{ID: role + "-" + content, Role: role, Content: aguix.TextContent(content)}
}

func input(runID string, messages ...aguix.Message) aguix.RunInput {
	return aguix.RunInput{ThreadID: "thread-1", RunID: runID, Messages: messages}
}

var fillField = aguix.ToolDefinition{
	Name:        "fill_field",
	Description: "Fill a form field on the page.",
	Parameters: json.RawMessage(`{"type":"object","properties":{"field":{"type":"string"}},` +
		`"required":["field"]}`),
}

func borrowCall(id string, book int) *genai.FunctionCall {
	return &genai.FunctionCall{ID: id, Name: "borrow_book", Args: map[string]any{"book_id": book}}
}

func fillCall(id string) *genai.FunctionCall {
	return &genai.FunctionCall{ID: id, Name: "fill_field", Args: map[string]any{"field": "email"}}
}

func wantTypes(t *testing.T, s served, want ...string) {
	t.Helper()
	if got := s.types(); !slices.Equal(got, want) {
		t.Errorf("frames:\n got %v\nwant %v\nbody:\n%s", got, want, s.body)
	}
}

func TestServerToolStreamsThroughTheRegistry(t *testing.T) {
	lib := &library{}
	llm := &scriptLLM{steps: []step{{text: "Let me check.", calls: []*genai.FunctionCall{borrowCall("c1", 7)}}}}
	s := serve(t, mustNew(t, librarian(t, llm, lib.toolset(t, adkx.Anonymous), ClientToolset())),
		input("run-1", text("user", "Borrow book 7.")))

	wantTypes(t, s, "RUN_STARTED",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_CONTENT",
		"TEXT_MESSAGE_END", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END",
		"RUN_FINISHED")
	if got := lib.ran(); !slices.Equal(got, []string{`book 7 for ""`}) {
		t.Errorf("the spec ran %v", got)
	}
	result := s.first("TOOL_CALL_RESULT")
	if result["content"] != `{"by":"","loan":70}` || result["outcome"] != nil {
		t.Errorf("result frame %v", result)
	}
	if _, has := s.last()["outcome"]; has {
		t.Errorf("RUN_FINISHED carries an outcome: %v", s.last())
	}
	if llm.calls() != 2 {
		t.Errorf("model called %d times, want 2", llm.calls())
	}
	golden(t, "server-tool", s)
}

func declarations(req *model.LLMRequest) map[string]*genai.FunctionDeclaration {
	out := map[string]*genai.FunctionDeclaration{}
	for _, tl := range req.Config.Tools {
		for _, d := range tl.FunctionDeclarations {
			if _, dup := out[d.Name]; dup {
				out[d.Name+" (again)"] = d
			}
			out[d.Name] = d
		}
	}
	return out
}

func TestBrowserToolEndsTheRunPendingAndItsAnswerReachesTheModel(t *testing.T) {
	llm := &scriptLLM{steps: []step{{text: "Filling it in.", calls: []*genai.FunctionCall{fillCall("c1")}}}}
	bridge := mustNew(t, librarian(t, llm, ClientToolset()))
	first := input("run-1", text("user", "Fill in my email."))
	first.Tools = []aguix.ToolDefinition{fillField}
	run1 := serve(t, bridge, first)

	wantTypes(t, run1, "RUN_STARTED",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_CONTENT",
		"TEXT_MESSAGE_END", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "RUN_FINISHED")
	outcome, _ := json.Marshal(run1.last()["outcome"])
	if string(outcome) != `{"pendingToolCallIds":["c1"],"type":"success"}` {
		t.Errorf("outcome %s", outcome)
	}
	decl := declarations(llm.request(0))["fill_field"]
	schema, _ := json.Marshal(decl.ParametersJsonSchema)
	if decl.Description != fillField.Description ||
		string(schema) != `{"properties":{"field":{"type":"string"}},"required":["field"],"type":"object"}` {
		t.Errorf("declared %q with %s", decl.Description, schema)
	}
	golden(t, "browser-tool-pending", run1)

	second := input("run-2", text("user", "Fill in my email."),
		aguix.Message{ID: "run-1-1", Role: "assistant", Content: aguix.TextContent("Filling it in."),
			ToolCalls: []aguix.ToolCall{{ID: "c1", Type: "function",
				Function: aguix.FunctionCall{Name: "fill_field", Arguments: `{"field":"email"}`}}}},
		aguix.Message{ID: "t1", Role: "tool", ToolCallID: "c1", Content: aguix.TextContent(`{"filled":true}`)},
	)
	second.Tools = []aguix.ToolDefinition{fillField}
	run2 := serve(t, bridge, second)
	wantTypes(t, run2, "RUN_STARTED",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END", "RUN_FINISHED")
	if start := run2.first("TEXT_MESSAGE_START"); start["messageId"] != "run-2-1" {
		t.Errorf("run 2's message id %v", start["messageId"])
	}
	contents := llm.request(1).Contents
	answer := contents[len(contents)-1].Parts[0].FunctionResponse
	if answer == nil || answer.ID != "c1" || answer.Name != "fill_field" || answer.Response["filled"] != true {
		t.Fatalf("the model did not receive the browser's answer last: %s", render(contents[len(contents)-1]))
	}
	golden(t, "browser-tool-answered", run2)
}

func TestMixedStepStopsAfterTheServerResult(t *testing.T) {
	lib := &library{}
	llm := &scriptLLM{steps: []step{{text: "Both.", calls: []*genai.FunctionCall{borrowCall("c1", 3), fillCall("c2")}}}}
	in := input("run-1", text("user", "Borrow book 3 and fill my email."))
	in.Tools = []aguix.ToolDefinition{fillField}
	s := serve(t, mustNew(t, librarian(t, llm, lib.toolset(t, adkx.Anonymous), ClientToolset())), in)

	if n := llm.calls(); n != 1 {
		t.Errorf("model called %d times, want 1: the client's call is unanswered", n)
	}
	if got := lib.ran(); len(got) != 1 {
		t.Errorf("the server tool ran %d times", len(got))
	}
	wantTypes(t, s, "RUN_STARTED",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END",
		"TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END",
		"TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END",
		"TOOL_CALL_RESULT", "RUN_FINISHED")
	if s.first("TOOL_CALL_RESULT")["toolCallId"] != "c1" {
		t.Errorf("result frame %v", s.first("TOOL_CALL_RESULT"))
	}
	outcome, _ := json.Marshal(s.last()["outcome"])
	if string(outcome) != `{"pendingToolCallIds":["c2"],"type":"success"}` {
		t.Errorf("outcome %s", outcome)
	}
	golden(t, "mixed-step", s)
}
