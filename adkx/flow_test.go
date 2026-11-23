package adkx_test

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"github.com/Artui/go-services/adkx"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

type scriptedLLM struct {
	calls []*genai.FunctionCall
	turn  int
	seen  [][]*genai.FunctionResponse
}

func (m *scriptedLLM) Name() string { return "scripted" }

func (m *scriptedLLM) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var frs []*genai.FunctionResponse
		for _, c := range req.Contents {
			for _, p := range c.Parts {
				if p.FunctionResponse != nil {
					frs = append(frs, p.FunctionResponse)
				}
			}
		}
		m.seen = append(m.seen, frs)
		if m.turn < len(m.calls) {
			call := m.calls[m.turn]
			m.turn++
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("done", "model")}, nil)
	}
}

func runThroughFlow(t *testing.T, ts tool.Toolset, calls ...*genai.FunctionCall) []*genai.FunctionResponse {
	t.Helper()
	llm := &scriptedLLM{calls: calls}
	a, err := llmagent.New(llmagent.Config{Name: "librarian", Model: llm, Toolsets: []tool.Toolset{ts}})
	if err != nil {
		t.Fatal(err)
	}
	svc := session.InMemoryService()
	r, err := runner.New(runner.Config{AppName: "app", Agent: a, SessionService: svc})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := svc.Create(t.Context(), &session.CreateRequest{AppName: "app", UserID: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	var out []*genai.FunctionResponse
	for ev, err := range r.Run(t.Context(), "ada", sess.Session.ID(), genai.NewContentFromText("go", "user"), agent.RunConfig{}) {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p.FunctionResponse != nil {
				out = append(out, p.FunctionResponse)
			}
		}
	}
	return out
}

func TestToolsRunThroughADKFlow(t *testing.T) {
	frs := runThroughFlow(t, mustToolset(t),
		&genai.FunctionCall{ID: "1", Name: "ping", Args: map[string]any{}},
		&genai.FunctionCall{ID: "2", Name: "borrow_book", Args: map[string]any{"book_id": 13}},
		&genai.FunctionCall{ID: "3", Name: "boom", Args: map[string]any{}},
	)
	got := map[string]string{}
	for _, fr := range frs {
		b, err := json.Marshal(fr.Response)
		if err != nil {
			t.Fatal(err)
		}
		got[fr.Name] = string(b)
	}
	if len(frs) != 3 {
		t.Fatalf("want 3 function responses, got %d: %v", len(frs), got)
	}
	if !strings.Contains(got["ping"], "pong") {
		t.Errorf("ping: %s", got["ping"])
	}
	if !strings.Contains(got["borrow_book"], "reference only") {
		t.Errorf("a refusal must reach the model verbatim: %s", got["borrow_book"])
	}
	if !strings.Contains(got["boom"], adkx.InternalErrorText) {
		t.Errorf("an internal error must be redacted: %s", got["boom"])
	}
}
