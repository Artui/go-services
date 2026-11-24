package aguiadk

import (
	"strings"
	"unicode/utf8"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/genai"
)

// streamedThought is a reasoning message sent from partials, and the text it
// carried, which the complete event's thought part must match to be sealed.
type streamedThought struct {
	id   string
	text *strings.Builder
}

// think appends a thinking delta, opening a reasoning span when none is open.
// Text and reasoning never interleave on the wire: each closes the other.
func (t *translator) think(out []aguix.Event, delta string) []aguix.Event {
	out = t.endText(out)
	if t.reasoning == "" {
		t.reasoning = t.newID()
		t.thoughts = append(t.thoughts, streamedThought{id: t.reasoning, text: &strings.Builder{}})
		out = append(out, aguix.ReasoningStart(t.reasoning), aguix.ReasoningMessageStart(t.reasoning))
	}
	t.thoughts[len(t.thoughts)-1].text.WriteString(delta)
	return append(out, aguix.ReasoningMessageContent(t.reasoning, delta))
}

func (t *translator) endReasoning(out []aguix.Event) []aguix.Event {
	if t.reasoning == "" {
		return out
	}
	out = append(out, aguix.ReasoningMessageEnd(t.reasoning), aguix.ReasoningEnd(t.reasoning))
	t.reasoning = ""
	return out
}

// settle matches a complete event's thought parts against the reasoning its
// partials streamed, and returns the indexes of the parts still to be sent.
//
// Partials carry no signature and no block boundary, so the signature reaches
// the client only here, as a REASONING_ENCRYPTED_VALUE on the message that
// already holds the text. That is sound only when the stream holds exactly one
// block: one reasoning message whose text is, byte for byte, the event's first
// thought part. The parts after it did not stream (a redacted part never does),
// so each is sent as a span of its own. Any other shape seals and sends
// nothing more, so the next run replays none of that turn's thinking rather
// than a block Claude would reject as modified.
func (t *translator) settle(out []aguix.Event, parts []*genai.Part) ([]aguix.Event, map[int]bool) {
	streamed := t.thoughts
	t.thoughts = nil
	var thoughts []int
	for i, p := range parts {
		if p != nil && p.Thought {
			thoughts = append(thoughts, i)
		}
	}
	spans := map[int]bool{}
	if len(streamed) == 0 {
		for _, i := range thoughts {
			spans[i] = true
		}
		return out, spans
	}
	if len(streamed) != 1 || len(thoughts) == 0 || parts[thoughts[0]].Text != streamed[0].text.String() {
		return out, spans
	}
	for _, i := range thoughts[1:] {
		spans[i] = true
	}
	return seal(out, streamed[0].id, parts[thoughts[0]].ThoughtSignature), spans
}

// span sends a thought part that did not stream as a whole reasoning span. A
// redacted part has no text, so its span is empty and only the seal says what
// it holds; an unsigned part with no text has nothing to say and is skipped.
func (t *translator) span(out []aguix.Event, p *genai.Part) []aguix.Event {
	if p.Text == "" && !sealable(p.ThoughtSignature) {
		return out
	}
	out = t.endText(out)
	id := t.newID()
	out = append(out, aguix.ReasoningStart(id), aguix.ReasoningMessageStart(id))
	if p.Text != "" {
		out = append(out, aguix.ReasoningMessageContent(id, p.Text))
	}
	out = append(out, aguix.ReasoningMessageEnd(id), aguix.ReasoningEnd(id))
	return seal(out, id, p.ThoughtSignature)
}

// seal sends a signature as the message's encrypted value, which the client
// keeps on the message and returns in the next run's history.
func seal(out []aguix.Event, id string, signature []byte) []aguix.Event {
	if !sealable(signature) {
		return out
	}
	return append(out, aguix.ReasoningEncryptedValue("message", id, string(signature)))
}

// sealable reports whether a signature survives JSON as it is. Claude's are
// ASCII; raw bytes from another provider would be rewritten, so are not sent.
func sealable(signature []byte) bool {
	return len(signature) > 0 && utf8.Valid(signature)
}
