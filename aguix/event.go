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

// ToolOutcome says how a tool call ended.
//
// TOOL_CALL_RESULT carries a content string and, as published, nothing else --
// no field saying whether the call succeeded. So a refusal and a success are
// the same frame with different words inside, and a client that wants to render
// them differently has to read the prose to find out which it got.
//
// This is the structural answer, serialised as "outcome". The vocabulary is
// pydantic-ai's own ToolReturnPart.outcome rather than one invented here, so
// the family's Python transport can forward what it already has and nothing in
// the chain has to translate. Its fourth member, "interrupted", is deliberately
// absent: it describes a run that was stopped rather than a call that ended,
// and it is not part of what a client is asked to render.
//
// The field is proposed upstream and emitted ahead of standardisation, which is
// a position rather than an accident: a consumer that reads it gains the
// distinction now, and one that does not sees exactly the stream it saw before.
// That second half is checked rather than assumed -- @ag-ui/core builds every
// event schema on a BaseEventSchema ending in .passthrough(), so an unrecognised
// key is neither stripped nor refused by the client's own parser. Verified at
// 0.0.59, the version the web component resolves; a schema that later turned
// strict would refuse this field, which is a thing to re-check rather than a
// thing to hope about.
type ToolOutcome string

const (
	// OutcomeSuccess is the absence of the field, and its value is the empty
	// string for exactly that reason: omitempty drops it.
	//
	// A successful result must not carry the key at all -- not even as
	// "success". A producer that has not adopted the field is indistinguishable
	// from one reporting a success, so absence is what the contract had to
	// mean; writing the word as well would suggest a client may require a key
	// that half the servers on this protocol will never send.
	//
	// It exists as a constant so that emitting a success is something a call
	// site says rather than something it leaves out.
	OutcomeSuccess ToolOutcome = ""

	// OutcomeFailed is a call that ran and failed: a bug, a definitive upstream
	// error, or a domain refusal such as a conflict or a missing row.
	OutcomeFailed ToolOutcome = "failed"

	// OutcomeDenied is a call refused by a person or a guard rather than
	// attempted. Nothing happened, and nothing the caller rephrases will change
	// that -- which is the half a client can act on differently.
	OutcomeDenied ToolOutcome = "denied"
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

	// Outcome is set only when the call did not succeed. It is declared last so
	// that a successful result's frame is byte for byte what it was before the
	// field existed, which is the compatibility claim stated as a property of
	// the struct rather than as a promise in a comment.
	Outcome ToolOutcome `json:"outcome,omitempty"`
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

// ToolCallResult reports what the call returned and how it ended.
//
// messageID identifies the tool message this result becomes in the thread, and
// is distinct from the tool call's own id: the protocol requires both.
//
// The outcome is a parameter rather than a second constructor because a caller
// that has an answer always knows which kind it is, and a signature that can be
// satisfied by saying nothing is one a later emit site will satisfy that way.
// Pass OutcomeSuccess, which is the empty value and therefore writes no field.
func ToolCallResult(messageID, toolCallID, content string, outcome ToolOutcome) Event {
	return Event{
		Type: EventToolCallResult, MessageID: messageID,
		ToolCallID: toolCallID, Content: content, Role: "tool",
		Outcome: outcome,
	}
}
