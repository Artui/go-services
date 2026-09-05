package aguix

import "encoding/json"

// EventType names one kind of AG-UI frame.
//
// The vocabulary is written from the published protocol rather than taken from
// a dependency.
//
// There is a community Go SDK for AG-UI. It is not used here, and the reason is
// not that it is bad: it carries no tags, so every consumer pins a different
// pseudo-version, and the pins found in the wild span nine months with no
// shared floor. Roughly seventy repositories reimplement these types inside
// their own frameworks rather than depend on it, which is the ecosystem's
// revealed preference. Depending on it would mean picking a private snapshot,
// not joining a community.
//
// What that costs is the obligation to be right about the wire. The field names
// below are the ones @ag-ui/core declares, verified against the package the web
// component actually resolves, and a test asserts the encoded frames rather
// than the structs.
type EventType string

// Only the events this package emits. The protocol has more -- state deltas,
// reasoning, activity, sub-agent lifecycle -- and adding one here is a decision
// to support it rather than a line in a list.
const (
	EventRunStarted  EventType = "RUN_STARTED"
	EventRunFinished EventType = "RUN_FINISHED"
	EventRunError    EventType = "RUN_ERROR"

	EventTextMessageStart   EventType = "TEXT_MESSAGE_START"
	EventTextMessageContent EventType = "TEXT_MESSAGE_CONTENT"
	EventTextMessageEnd     EventType = "TEXT_MESSAGE_END"

	EventToolCallStart  EventType = "TOOL_CALL_START"
	EventToolCallArgs   EventType = "TOOL_CALL_ARGS"
	EventToolCallEnd    EventType = "TOOL_CALL_END"
	EventToolCallResult EventType = "TOOL_CALL_RESULT"
)

// Event is one frame on the wire.
//
// It is a single struct with omitempty fields rather than one type per event.
// The protocol's own discriminator is the type field and every event is a flat
// object, so a union here would buy type safety at the cost of a conversion
// layer that could disagree with the wire -- and this package's whole risk is
// disagreeing with the wire. What guards it instead is that emitting is done
// through the constructors below, which no caller can bypass without saying so.
type Event struct {
	Type EventType `json:"type"`

	// Run lifecycle.
	ThreadID string `json:"threadId,omitempty"`
	RunID    string `json:"runId,omitempty"`

	// RUN_ERROR. Message is required by the schema, so it is not omitempty:
	// a RUN_ERROR carrying no message is refused by the client's own parser.
	Message string `json:"message,omitempty"`
	Code    string `json:"code,omitempty"`

	// Text messages.
	MessageID string `json:"messageId,omitempty"`
	Role      string `json:"role,omitempty"`
	Delta     string `json:"delta,omitempty"`

	// Tool calls.
	ToolCallID      string `json:"toolCallId,omitempty"`
	ToolCallName    string `json:"toolCallName,omitempty"`
	ParentMessageID string `json:"parentMessageId,omitempty"`

	// TOOL_CALL_RESULT. Content is a string on this wire even when the tool
	// returned an object: the protocol says so, and the client renders it as
	// the tool's textual answer.
	Content string `json:"content,omitempty"`
}

// RunStarted opens a run. Every stream begins with one, and a client that
// receives anything else first treats the run as malformed.
func RunStarted(threadID, runID string) Event {
	return Event{Type: EventRunStarted, ThreadID: threadID, RunID: runID}
}

// RunFinished closes a run that completed.
func RunFinished(threadID, runID string) Event {
	return Event{Type: EventRunFinished, ThreadID: threadID, RunID: runID}
}

// RunError closes a run that failed.
//
// The message reaches the user. An agent's internal failure is written for an
// operator, so a handler redacts before it gets here -- see Handler.
func RunError(message string) Event {
	return Event{Type: EventRunError, Message: message}
}

// TextMessageStart opens an assistant message. The id ties the parts together
// and must be unique within the run.
func TextMessageStart(messageID string) Event {
	return Event{Type: EventTextMessageStart, MessageID: messageID, Role: "assistant"}
}

// TextMessageContent appends to an open message.
func TextMessageContent(messageID, delta string) Event {
	return Event{Type: EventTextMessageContent, MessageID: messageID, Delta: delta}
}

// TextMessageEnd closes an open message.
func TextMessageEnd(messageID string) Event {
	return Event{Type: EventTextMessageEnd, MessageID: messageID}
}

// ToolCallStart announces a call the agent is making.
func ToolCallStart(toolCallID, name, parentMessageID string) Event {
	return Event{
		Type: EventToolCallStart, ToolCallID: toolCallID,
		ToolCallName: name, ParentMessageID: parentMessageID,
	}
}

// ToolCallArgs streams the call's arguments as JSON text.
//
// The protocol streams them as a string because a model produces them a token
// at a time. This package has the whole object before it starts, so it sends
// one frame -- which is valid and is what a client reassembling the string
// expects either way.
func ToolCallArgs(toolCallID string, args json.RawMessage) Event {
	return Event{Type: EventToolCallArgs, ToolCallID: toolCallID, Delta: string(args)}
}

// ToolCallEnd closes the argument stream for a call.
func ToolCallEnd(toolCallID string) Event {
	return Event{Type: EventToolCallEnd, ToolCallID: toolCallID}
}

// ToolCallResult reports what the call returned.
//
// messageID identifies the tool message this result becomes in the thread, and
// is distinct from the tool call's own id: the protocol requires both.
func ToolCallResult(messageID, toolCallID, content string) Event {
	return Event{
		Type: EventToolCallResult, MessageID: messageID,
		ToolCallID: toolCallID, Content: content, Role: "tool",
	}
}
