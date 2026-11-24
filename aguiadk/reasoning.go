package aguiadk

import (
	"encoding/json"
	"strings"

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
// Partials carry no signature and no block boundary, so a stream that held
// several blocks reads as one reasoning message, and the signatures reach the
// client only here, as a REASONING_ENCRYPTED_VALUE on that message. It is sound
// only when the stream holds exactly one reasoning message whose text is, byte
// for byte, the event's thought parts joined in order (a redacted part never
// streams, and adds no text): the seal then lists every part, so the next run
// can split the text back into the blocks Claude sent. Any other shape seals
// and sends nothing more, so the next run replays none of that turn's thinking
// rather than a block Claude would reject as modified.
func (t *translator) settle(out []aguix.Event, parts []*genai.Part) ([]aguix.Event, map[int]bool) {
	streamed := t.thoughts
	t.thoughts = nil
	spans := map[int]bool{}
	var (
		thoughts []*genai.Part
		joined   strings.Builder
	)
	for i, p := range parts {
		if p != nil && p.Thought {
			spans[i] = true
			thoughts = append(thoughts, p)
			joined.WriteString(p.Text)
		}
	}
	if len(streamed) == 0 {
		return out, spans
	}
	clear(spans)
	if len(streamed) != 1 || joined.String() != streamed[0].text.String() {
		return out, spans
	}
	return seal(out, streamed[0].id, thoughts...), spans
}

// span sends a thought part that did not stream as a whole reasoning span. A
// redacted part has no text, so its span is empty and only the seal says what
// it holds; an unsigned part with no text has nothing to say and is skipped.
func (t *translator) span(out []aguix.Event, p *genai.Part) []aguix.Event {
	if p.Text == "" && len(p.ThoughtSignature) == 0 {
		return out
	}
	out = t.endText(out)
	id := t.newID()
	out = append(out, aguix.ReasoningStart(id), aguix.ReasoningMessageStart(id))
	if p.Text != "" {
		out = append(out, aguix.ReasoningMessageContent(id, p.Text))
	}
	out = append(out, aguix.ReasoningMessageEnd(id), aguix.ReasoningEnd(id))
	return seal(out, id, p)
}

// sealed is a reasoning message's encrypted value: the thinking blocks its
// text holds, in order, each as the bytes of that text it covers and its
// signature. The client keeps it opaque and returns it in the next run.
type sealed struct {
	Blocks []sealedBlock `json:"blocks"`
}

// sealedBlock's signature is base64 in JSON, so any bytes a provider signs
// with survive the round trip unchanged.
type sealedBlock struct {
	Length    int    `json:"length"`
	Signature []byte `json:"signature,omitempty"`
}

// seal sends the blocks of a message as its encrypted value, unless none of
// them is signed, which leaves nothing the next run could send back.
func seal(out []aguix.Event, id string, blocks ...*genai.Part) []aguix.Event {
	var value sealed
	signed := false
	for _, p := range blocks {
		if p.Text == "" && len(p.ThoughtSignature) == 0 {
			continue
		}
		signed = signed || len(p.ThoughtSignature) > 0
		value.Blocks = append(value.Blocks, sealedBlock{Length: len(p.Text), Signature: p.ThoughtSignature})
	}
	if !signed {
		return out
	}
	raw, _ := json.Marshal(value)
	return append(out, aguix.ReasoningEncryptedValue("message", id, string(raw)))
}

// unseal splits a reasoning message back into the thinking blocks its seal
// lists. A value it cannot read, or whose lengths do not cover the text
// exactly, as another server's might not, yields nothing.
func unseal(m aguix.Message) []*genai.Part {
	var value sealed
	if json.Unmarshal([]byte(m.EncryptedValue), &value) != nil {
		return nil
	}
	text, at := m.Text(), 0
	var parts []*genai.Part
	for _, b := range value.Blocks {
		if b.Length < 0 || b.Length > len(text)-at {
			return nil
		}
		parts = append(parts, &genai.Part{Thought: true, Text: text[at : at+b.Length], ThoughtSignature: b.Signature})
		at += b.Length
	}
	if at != len(text) {
		return nil
	}
	return parts
}
