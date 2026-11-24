package aguiadk

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Artui/go-services/adkx"
	"github.com/Artui/go-services/aguix"
	"google.golang.org/genai"
)

func usageOf(t *testing.T, s served) string {
	t.Helper()
	finished := s.last()
	if finished["type"] != "RUN_FINISHED" {
		t.Fatalf("the run did not finish: %s", s.body)
	}
	raw, _ := json.Marshal(finished["usage"])
	return string(raw)
}

func TestUsageIsSummedOverTheInvocation(t *testing.T) {
	lib := &library{}
	llm := &scriptLLM{steps: []step{
		{text: "Let me check.", calls: []*genai.FunctionCall{borrowCall("c1", 7)}, model: "m-1",
			usage: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount: 10, ToolUsePromptTokenCount: 1, CandidatesTokenCount: 4,
				ThoughtsTokenCount: 2, CachedContentTokenCount: 3, TotalTokenCount: 17,
			}},
		{text: "Lent.", model: "m-1", usage: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount: 20, CandidatesTokenCount: 5, TotalTokenCount: 25,
		}},
	}}
	s := serve(t, mustNew(t, librarian(t, llm, lib.toolset(t, adkx.Anonymous))),
		input("run-usage", text("user", "Borrow book 7.")))

	want := `[{"cachedInputTokens":3,"inputTokens":31,"model":"m-1","outputTokens":11,` +
		`"reasoningTokens":2,"totalTokens":42}]`
	if got := usageOf(t, s); got != want {
		t.Errorf("usage:\n got %s\nwant %s", got, want)
	}
	if llm.calls() != 2 {
		t.Errorf("model called %d times, want 2", llm.calls())
	}
	golden(t, "usage-two-calls", s)
}

func TestUsageKeepsOneEntryPerModelInTheOrderSeen(t *testing.T) {
	lib := &library{}
	llm := &scriptLLM{steps: []step{
		{calls: []*genai.FunctionCall{borrowCall("c1", 7)}, model: "b",
			usage: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 1}},
		{calls: []*genai.FunctionCall{borrowCall("c2", 8)}, model: "a",
			usage: &genai.GenerateContentResponseUsageMetadata{CandidatesTokenCount: 2}},
		{text: "Lent.", model: "b", usage: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 4}},
	}}
	s := serve(t, mustNew(t, librarian(t, llm, lib.toolset(t, adkx.Anonymous))),
		input("run-models", text("user", "Borrow books 7 and 8.")))

	if got, want := usageOf(t, s), `[{"inputTokens":5,"model":"b"},{"model":"a","outputTokens":2}]`; got != want {
		t.Errorf("usage:\n got %s\nwant %s", got, want)
	}
}

func TestARunWithoutUsageFinishesWithout(t *testing.T) {
	s := serve(t, mustNew(t, librarian(t, &scriptLLM{})), input("run-none", text("user", "Hi.")))
	if _, has := s.last()["usage"]; has || s.last()["type"] != "RUN_FINISHED" {
		t.Errorf("last frame %v", s.last())
	}
}

func TestUsageSurvivesARunTimeout(t *testing.T) {
	lib := &library{}
	llm := &scriptLLM{steps: []step{
		{calls: []*genai.FunctionCall{borrowCall("c1", 7)},
			usage: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 6, CandidatesTokenCount: 2}},
		{block: true},
	}}
	in := input("run-timeout", text("user", "Borrow book 7."))
	in.ProtocolVersion = "1.0"
	s := serveWith(t, mustNew(t, librarian(t, llm, lib.toolset(t, adkx.Anonymous))), in,
		[]aguix.Option{aguix.WithRunTimeout(200 * time.Millisecond)})

	if o, _ := s.last()["outcome"].(map[string]any); o["type"] != "cancelled" {
		t.Fatalf("last frame %v", s.last())
	}
	if got, want := usageOf(t, s), `[{"inputTokens":6,"outputTokens":2}]`; got != want {
		t.Errorf("usage:\n got %s\nwant %s", got, want)
	}
}

func TestANegativeTokenCountFailsTheRun(t *testing.T) {
	llm := &scriptLLM{steps: []step{{text: "Hi.",
		usage: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: -1, CandidatesTokenCount: 2}}}}
	s := serve(t, mustNew(t, librarian(t, llm)), input("run-negative", text("user", "Hi.")))

	if last := s.last(); last["type"] != "RUN_ERROR" || last["usage"] != nil {
		t.Errorf("last frame %v", last)
	}
	if len(s.errs) != 1 {
		t.Errorf("errors %v", s.errs)
	}
}

func TestMaxLLMCallsBoundsTheInvocation(t *testing.T) {
	lib := &library{}
	llm := &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{borrowCall("c1", 7)}}}}
	agent := mustNew(t, librarian(t, llm, lib.toolset(t, adkx.Anonymous)), WithMaxLLMCalls(1))
	s := serve(t, agent, input("run-limit", text("user", "Borrow book 7.")))

	if last := s.last(); last["type"] != "RUN_ERROR" || last["code"] != aguix.CodeError {
		t.Errorf("last frame %v", last)
	}
	if llm.calls() != 1 {
		t.Errorf("model called %d times, want 1", llm.calls())
	}
}
