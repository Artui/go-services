package aguiadk

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Artui/go-services/aguix"
)

// recorder rebuilds the messages a run sent from its frames, as the client's
// own reducer files them, so a stored thread reads back as the page showed it:
// under the wire's message and call ids, with each reasoning span's signature.
// Activities, state and custom events are not messages and are not kept.
type recorder struct {
	msgs []*draft
}

type draft struct {
	msg     aguix.Message
	text    string
	hasText bool
	args    []string
}

func (r *recorder) observe(e aguix.Event) {
	switch e.Type {
	case aguix.EventTextMessageStart:
		role := e.Role
		if role == "" {
			role = "assistant"
		}
		r.open(aguix.Message{ID: e.MessageID, Role: role})
	case aguix.EventTextMessageContent, aguix.EventReasoningMessageContent:
		if d := r.find(e.MessageID); d != nil {
			d.text += e.Delta
		}
	case aguix.EventReasoningMessageStart:
		r.open(aguix.Message{ID: e.MessageID, Role: "reasoning"})
	case aguix.EventReasoningEncryptedValue:
		r.seal(e)
	case aguix.EventToolCallStart:
		r.call(e)
	case aguix.EventToolCallArgs:
		if d, i := r.findCall(e.ToolCallID); d != nil {
			d.args[i] += e.Delta
		}
	case aguix.EventToolCallResult:
		r.result(e)
	}
}

// result files a tool message after the call's message and the answers already
// behind it, where the client puts it, with a failed call's outcome in its
// metadata as the client keeps it.
func (r *recorder) result(e aguix.Event) {
	d := &draft{msg: aguix.Message{ID: e.MessageID, Role: "tool", ToolCallID: e.ToolCallID}, hasText: true, text: e.Content}
	if e.Outcome != aguix.OutcomeSuccess {
		d.msg.Metadata = json.RawMessage(fmt.Sprintf(`{"outcome":%q}`, e.Outcome))
	}
	at := len(r.msgs)
	if i := slices.IndexFunc(r.msgs, func(m *draft) bool {
		return slices.ContainsFunc(m.msg.ToolCalls, func(c aguix.ToolCall) bool { return c.ID == e.ToolCallID })
	}); i >= 0 {
		at = i + 1
		for at < len(r.msgs) && r.msgs[at].msg.Role == "tool" {
			at++
		}
	}
	r.msgs = slices.Insert(r.msgs, at, d)
}

func (r *recorder) open(m aguix.Message) *draft {
	d := &draft{msg: m, hasText: true}
	r.msgs = append(r.msgs, d)
	return d
}

func (r *recorder) find(id string) *draft {
	for i := len(r.msgs) - 1; i >= 0; i-- {
		if r.msgs[i].msg.ID == id {
			return r.msgs[i]
		}
	}
	return nil
}

// call files a call on the assistant message it names when that message is the
// newest one, and otherwise on a new assistant message of its own.
func (r *recorder) call(e aguix.Event) {
	var d *draft
	if n := len(r.msgs); n > 0 && e.ParentMessageID != "" &&
		r.msgs[n-1].msg.ID == e.ParentMessageID && r.msgs[n-1].msg.Role == "assistant" {
		d = r.msgs[n-1]
	} else {
		id := e.ParentMessageID
		if id == "" {
			id = e.ToolCallID
		}
		d = r.open(aguix.Message{ID: id, Role: "assistant"})
		d.hasText = false
	}
	d.msg.ToolCalls = append(d.msg.ToolCalls, aguix.ToolCall{
		ID: e.ToolCallID, Type: "function", Function: aguix.FunctionCall{Name: e.ToolCallName},
	})
	d.args = append(d.args, "")
}

func (r *recorder) findCall(id string) (*draft, int) {
	for i := len(r.msgs) - 1; i >= 0; i-- {
		if j := slices.IndexFunc(r.msgs[i].msg.ToolCalls, func(c aguix.ToolCall) bool { return c.ID == id }); j >= 0 {
			return r.msgs[i], j
		}
	}
	return nil, 0
}

func (r *recorder) seal(e aguix.Event) {
	if e.Subtype == "tool-call" {
		if d, i := r.findCall(e.EntityID); d != nil {
			d.msg.ToolCalls[i].EncryptedValue = e.EncryptedValue
		}
		return
	}
	if d := r.find(e.EntityID); d != nil {
		d.msg.EncryptedValue = e.EncryptedValue
	}
}

// messages returns what the run sent, in the order it sent it.
func (r *recorder) messages() []aguix.Message {
	out := make([]aguix.Message, 0, len(r.msgs))
	for _, d := range r.msgs {
		m := d.msg
		if d.hasText {
			m.Content = aguix.TextContent(d.text)
		}
		m.ToolCalls = slices.Clone(m.ToolCalls)
		for j := range m.ToolCalls {
			m.ToolCalls[j].Function.Arguments = d.args[j]
		}
		out = append(out, m)
	}
	return out
}
