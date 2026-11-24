package aguiadk

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Artui/go-services/adkx"
	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
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
	if len(s.errs) != 1 || !strings.Contains(s.errs[0].Error(), "outside 0 to 2^53-1") {
		t.Errorf("errors %v", s.errs)
	}
}

func TestMaxLLMCallsBoundsTheInvocation(t *testing.T) {
	lib := &library{}
	llm := &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{borrowCall("c1", 7)}}}}
	bounded := mustNew(t, librarian(t, llm, lib.toolset(t, adkx.Anonymous)), WithMaxLLMCalls(1))
	s := serve(t, bounded, input("run-limit", text("user", "Borrow book 7.")))

	if last := s.last(); last["type"] != "RUN_ERROR" || last["code"] != aguix.CodeError {
		t.Errorf("last frame %v", last)
	}
	if llm.calls() != 1 {
		t.Errorf("model called %d times, want 1", llm.calls())
	}
	if len(s.errs) != 1 || !errors.Is(s.errs[0], agent.ErrLLMCallsLimitExceeded) {
		t.Errorf("errors %v", s.errs)
	}
}

// usageFor runs one model answer carrying u and custom, and returns its usage.
func usageFor(t *testing.T, u *genai.GenerateContentResponseUsageMetadata, custom map[string]any) string {
	t.Helper()
	llm := &scriptLLM{steps: []step{{text: "Hi.", usage: u, custom: custom}}}
	return usageOf(t, serve(t, mustNew(t, librarian(t, llm)), input("run-shape", text("user", "Hi."))))
}

// ADK's OpenAI models count reasoning inside the candidates and again as thoughts.
func TestOpenAIShapedUsageCountsReasoningOnce(t *testing.T) {
	got := usageFor(t, &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: 100, CandidatesTokenCount: 20, ThoughtsTokenCount: 7,
		CachedContentTokenCount: 40, TotalTokenCount: 120,
	}, nil)
	want := `[{"cachedInputTokens":40,"inputTokens":100,"outputTokens":20,"reasoningTokens":7,"totalTokens":120}]`
	if got != want {
		t.Errorf("usage:\n got %s\nwant %s", got, want)
	}
}

// ADK's Anthropic model keeps thinking out of the candidates and puts cache writes in its metadata.
func TestAnthropicShapedUsageCarriesCacheWrites(t *testing.T) {
	got := usageFor(t, &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: 100, CandidatesTokenCount: 10, ThoughtsTokenCount: 5,
		CachedContentTokenCount: 30, TotalTokenCount: 115,
	}, map[string]any{cacheWriteKey: int64(20)})
	want := `[{"cacheWriteInputTokens":20,"cachedInputTokens":30,"inputTokens":100,"outputTokens":15,` +
		`"reasoningTokens":5,"totalTokens":115}]`
	if got != want {
		t.Errorf("usage:\n got %s\nwant %s", got, want)
	}
}

func TestATotalBelowTheInputFallsBackToTheParts(t *testing.T) {
	got := usageFor(t, &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: 10, CandidatesTokenCount: 3, ThoughtsTokenCount: 1, TotalTokenCount: 5,
	}, map[string]any{cacheWriteKey: "20"})
	if want := `[{"inputTokens":10,"outputTokens":4,"reasoningTokens":1,"totalTokens":5}]`; got != want {
		t.Errorf("usage:\n got %s\nwant %s", got, want)
	}
}

func TestUsageSurvivesARunThatStopsForTheClient(t *testing.T) {
	llm := &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{fillCall("f1")},
		usage: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 8, CandidatesTokenCount: 3}}}}
	in := input("run-client", text("user", "Fill the email."))
	in.Tools = []aguix.ToolDefinition{fillField}
	s := serve(t, mustNew(t, librarian(t, llm, ClientToolset())), in)

	if o, _ := s.last()["outcome"].(map[string]any); o["pendingToolCallIds"] == nil {
		t.Fatalf("last frame %v", s.last())
	}
	if got, want := usageOf(t, s), `[{"inputTokens":8,"outputTokens":3}]`; got != want {
		t.Errorf("usage:\n got %s\nwant %s", got, want)
	}
}
