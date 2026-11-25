package aguiadk

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Artui/go-services/aguix"
)

// recorder rebuilds the messages a run sent from its frames, as the client's
// own reducer files them, so a stored thread reads back as the page showed it:
// under the wire's message and call ids, with each reasoning span's signature
// and each activity's last snapshot. State and custom events are not messages.
type recorder struct {
	prior []aguix.Message
	msgs  []*draft
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
	case aguix.EventActivitySnapshot:
		r.activity(e)
	}
}

// activity files a snapshot as the client does: a new id goes last, and a
// message the id already names, in the run or before it, is replaced in place
// unless replace is false.
func (r *recorder) activity(e aguix.Event) {
	encoded, _ := json.Marshal(e)
	var frame struct {
		Content json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(encoded, &frame)
	m := aguix.Message{
		ID: e.MessageID, Role: "activity", ActivityType: e.ActivityType, Content: frame.Content, SubagentRunID: e.SubagentRunID,
	}
	replace := e.Replace == nil || *e.Replace
	if j := slices.IndexFunc(r.prior, func(p aguix.Message) bool { return p.ID == e.MessageID }); j >= 0 {
		if replace {
			r.prior[j] = m
		}
		return
	}
	i := slices.IndexFunc(r.msgs, func(d *draft) bool { return d.msg.ID == e.MessageID })
	switch {
	case i < 0:
		r.msgs = append(r.msgs, &draft{msg: m})
	case replace:
		r.msgs[i] = &draft{msg: m}
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

// call files a call as the client's reducer does: a call it already holds is
// renamed; otherwise it goes on the assistant message its parent id names,
// wherever that is, or on a new assistant message under the parent id, or under
// the call's own id when there is no parent or the parent is not an assistant.
func (r *recorder) call(e aguix.Event) {
	if d, i := r.findCall(e.ToolCallID); d != nil {
		d.msg.ToolCalls[i].Function.Name = e.ToolCallName
		return
	}
	id := e.ToolCallID
	var d *draft
	if e.ParentMessageID != "" {
		i := slices.IndexFunc(r.msgs, func(m *draft) bool { return m.msg.ID == e.ParentMessageID })
		switch {
		case i < 0:
			id = e.ParentMessageID
		case r.msgs[i].msg.Role == "assistant":
			d = r.msgs[i]
		}
	}
	if d == nil {
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
