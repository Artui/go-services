package aguix

import (
	"encoding/json"
	"strings"
)

// transcript folds a run's frames back into the messages they streamed, the
// way django-ag-ui's RunTranscript does: assistant text and tool calls, and
// tool results. Reasoning, activities, state and custom frames are not messages
// here; a frame for a call that never started is ignored, and so is a tool frame
// a sub-agent made, which is not a call the parent's assistant made. A sub-agent's
// text keeps its SubagentRunID.
type transcript struct {
	items   []*transcriptItem
	pending map[string]*pendingCall
}

// transcriptItem is an assistant message being built, or a finished tool
// message.
type transcriptItem struct {
	id       string
	subagent string
	text     strings.Builder
	calls    []ToolCall
	tool     *Message
}

var toolFrames = map[EventType]bool{
	EventToolCallStart: true, EventToolCallArgs: true, EventToolCallEnd: true, EventToolCallResult: true,
}

type pendingCall struct {
	name   string
	parent string
	args   strings.Builder
}

func newTranscript() *transcript {
	return &transcript{pending: map[string]*pendingCall{}}
}

func (t *transcript) add(ev Event) {
	if ev.SubagentRunID != "" && toolFrames[ev.Type] {
		return
	}
	switch ev.Type {
	case EventTextMessageStart:
		t.draft(ev.MessageID).subagent = ev.SubagentRunID
	case EventTextMessageContent:
		t.draft(ev.MessageID).text.WriteString(ev.Delta)
	case EventToolCallStart:
		t.pending[ev.ToolCallID] = &pendingCall{name: ev.ToolCallName, parent: ev.ParentMessageID}
	case EventToolCallArgs:
		if call, ok := t.pending[ev.ToolCallID]; ok {
			call.args.WriteString(ev.Delta)
		}
	case EventToolCallEnd:
		call, ok := t.pending[ev.ToolCallID]
		if !ok {
			return
		}
		delete(t.pending, ev.ToolCallID)
		owner := call.parent
		if owner == "" {
			owner = ev.ToolCallID
		}
		draft := t.draft(owner)
		draft.calls = append(draft.calls, ToolCall{
			ID: ev.ToolCallID, Type: "function",
			Function: FunctionCall{Name: call.name, Arguments: call.args.String()},
		})
	case EventToolCallResult:
		t.items = append(t.items, &transcriptItem{tool: toolMessage(ev)})
	}
}

// draft is the newest assistant message under id, opened if there is none.
func (t *transcript) draft(id string) *transcriptItem {
	for i := len(t.items) - 1; i >= 0; i-- {
		if item := t.items[i]; item.tool == nil && item.id == id {
			return item
		}
	}
	item := &transcriptItem{id: id}
	t.items = append(t.items, item)
	return item
}

// messages lists what streamed, in stream order, leaving out any assistant
// message that carried neither text nor a call.
func (t *transcript) messages() []Message {
	var out []Message
	for _, item := range t.items {
		if item.tool != nil {
			out = append(out, *item.tool)
			continue
		}
		text := item.text.String()
		if text == "" && len(item.calls) == 0 {
			continue
		}
		message := Message{
			ID: item.id, Role: "assistant", ToolCalls: item.calls, SubagentRunID: item.subagent,
		}
		if text != "" {
			message.Content = TextContent(text)
		}
		out = append(out, message)
	}
	return out
}

// toolMessage keeps a result's metadata, which is where a failed or denied
// call's outcome rides, so a replayed thread reads what the stream said.
func toolMessage(ev Event) *Message {
	message := &Message{
		ID: ev.MessageID, Role: "tool", Content: TextContent(ev.Content), ToolCallID: ev.ToolCallID,
	}
	if len(ev.Metadata) > 0 {
		message.Metadata, _ = json.Marshal(ev.Metadata) // it encoded on the wire a moment ago
	}
	return message
}
