package aguiadk

import (
	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/session"
)

// tally sums the token usage a run's model calls report, one entry per model
// in the order the models first answered.
//
// AG-UI counts input and output as totals with reasoning and cache reads as
// parts of them, while genai keeps the model's thoughts and a tool-use prompt
// apart from the candidates and the prompt, so those are added in. genai has no
// count of cache writes, and an event names its model but not the provider.
type tally struct {
	entries []aguix.TokenUsage
}

// add counts ev's usage. A partial event is skipped: under SSE a provider may
// repeat a running count on its partials, and the complete event carries the
// call's own.
func (t *tally) add(ev *session.Event) bool {
	u := ev.UsageMetadata
	if ev.Partial || u == nil {
		return false
	}
	i := 0
	for i < len(t.entries) && t.entries[i].Model != ev.ModelVersion {
		i++
	}
	if i == len(t.entries) {
		t.entries = append(t.entries, aguix.TokenUsage{Model: ev.ModelVersion})
	}
	e := &t.entries[i]
	e.InputTokens += int64(u.PromptTokenCount) + int64(u.ToolUsePromptTokenCount)
	e.OutputTokens += int64(u.CandidatesTokenCount) + int64(u.ThoughtsTokenCount)
	e.TotalTokens += int64(u.TotalTokenCount)
	e.ReasoningTokens += int64(u.ThoughtsTokenCount)
	e.CachedInputTokens += int64(u.CachedContentTokenCount)
	return true
}
