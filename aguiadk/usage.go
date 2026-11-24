package aguiadk

import (
	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/session"
)

// cacheWriteKey is where ADK's anthropicmodel puts its cache-creation count, an
// int64, since genai has no field for it (anthropicmodel.CacheCreationInputTokensMetadataKey).
const cacheWriteKey = "anthropic_cache_creation_input_tokens"

// tally sums the token usage a run's model calls report, one entry per model
// in the order the models first answered.
//
// AG-UI counts input and output as totals with reasoning and cache counts as
// parts of them. genai's input is the prompt plus any tool-use prompt; its
// output is the total less that, because ADK's providers disagree on whether
// the candidates include the thoughts (OpenAI's do, Gemini's and Anthropic's do
// not). Without a usable total it is the candidates plus the thoughts. An event
// names its model but not the provider.
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
	input, total := int64(u.PromptTokenCount)+int64(u.ToolUsePromptTokenCount), int64(u.TotalTokenCount)
	output := int64(u.CandidatesTokenCount) + int64(u.ThoughtsTokenCount)
	if total > 0 && total >= input {
		output = total - input
	}
	e.InputTokens += input
	e.OutputTokens += output
	e.TotalTokens += total
	e.ReasoningTokens += int64(u.ThoughtsTokenCount)
	e.CachedInputTokens += int64(u.CachedContentTokenCount)
	if written, ok := ev.CustomMetadata[cacheWriteKey].(int64); ok {
		e.CacheWriteInputTokens += written
	}
	return true
}
