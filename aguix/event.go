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

// Only the events this package emits. The protocol has more -- sub-agent
// lifecycle, steps, raw -- and adding one here is a decision to support it
// rather than a line in a list.
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

	EventStateSnapshot    EventType = "STATE_SNAPSHOT"
	EventStateDelta       EventType = "STATE_DELTA"
	EventMessagesSnapshot EventType = "MESSAGES_SNAPSHOT"

	EventReasoningStart          EventType = "REASONING_START"
	EventReasoningMessageStart   EventType = "REASONING_MESSAGE_START"
	EventReasoningMessageContent EventType = "REASONING_MESSAGE_CONTENT"
	EventReasoningMessageEnd     EventType = "REASONING_MESSAGE_END"
	EventReasoningEnd            EventType = "REASONING_END"
	EventReasoningEncryptedValue EventType = "REASONING_ENCRYPTED_VALUE"

	EventActivitySnapshot EventType = "ACTIVITY_SNAPSHOT"
	EventActivityDelta    EventType = "ACTIVITY_DELTA"

	EventCustom EventType = "CUSTOM"
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
// The field is proposed upstream and emitted ahead of standardisation, and it
// is written twice, under the one key, as the family's Python transport writes
// it:
//
//   - In the event's metadata, which is what a client on @ag-ui/client 1.0 reads,
//     @artooi/agents-web-components 0.4.0 among them. Metadata is the one slot on an
//     event that 1.0 declares open by key, and the client folds a result's
//     metadata onto the tool message it appends, so the outcome survives into
//     the saved thread.
//   - At the top level, which is what a 0.x client read. 1.0 does not keep it:
//     its enforcement stage strips every key a schema does not declare before
//     any subscriber runs, which is why a server writing only this copy has its
//     denied calls rendered as done.
//
// Either way a client that reads neither sees the stream it saw before.
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

	// Metadata is the protocol's open slot on an event. It is declared after
	// Outcome for the same reason, and a constructor sets it only when it has
	// something to say, so a frame without it is the frame it always was.
	Metadata map[string]any `json:"metadata,omitempty"`

	// RUN_FINISHED's token usage, one entry per provider and model. Absent
	// unless the agent reported some through Stream.SetUsage.
	Usage []TokenUsage `json:"usage,omitempty"`

	// CUSTOM's name.
	Name string `json:"name,omitempty"`

	// ACTIVITY_SNAPSHOT and ACTIVITY_DELTA. Replace is a pointer because absent
	// and false differ: the client defaults an absent one to true.
	ActivityType string `json:"activityType,omitempty"`
	Replace      *bool  `json:"replace,omitempty"`

	// REASONING_ENCRYPTED_VALUE: whether the handle belongs to a "message" or a
	// "tool-call", which entity, and the provider's opaque value.
	Subtype        string `json:"subtype,omitempty"`
	EntityID       string `json:"entityId,omitempty"`
	EncryptedValue string `json:"encryptedValue,omitempty"`

	// The one object-valued slot an event has, written under the key its type
	// names (see payloadKeys). It is not a field because the keys collide with
	// the string ones above: STATE_DELTA's "delta" is an array and
	// ACTIVITY_SNAPSHOT's "content" an object, where TEXT_MESSAGE_CONTENT's
	// and TOOL_CALL_RESULT's are strings.
	payload    any
	hasPayload bool
}

// payloadKeys names where each payload-carrying event writes its payload.
var payloadKeys = map[EventType]string{
	EventStateSnapshot:    "snapshot",
	EventStateDelta:       "delta",
	EventMessagesSnapshot: "messages",
	EventActivitySnapshot: "content",
	EventActivityDelta:    "patch",
	EventCustom:           "value",
	EventRunFinished:      "outcome",
}

type plainEvent Event

// MarshalJSON writes the flat fields, then the payload under its key.
//
// A payload that cannot be encoded fails here, so Emit returns the error
// instead of writing a STATE_SNAPSHOT without its snapshot, which the client's
// parser refuses and which would end the run on the client's side alone.
func (e Event) MarshalJSON() ([]byte, error) {
	flat, err := json.Marshal(plainEvent(e))
	if err != nil || !e.hasPayload {
		return flat, err
	}
	payload, err := json.Marshal(e.payload)
	if err != nil {
		return nil, err
	}
	out := append(flat[:len(flat)-1], `,"`+payloadKeys[e.Type]+`":`...)
	return append(append(out, payload...), '}'), nil
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

// The codes a handler puts on RUN_ERROR, in the vocabulary the family's Python
// transport uses, so a client reads one meaning from either server. The code
// survives redaction: it says what kind of failure it was, never why.
const (
	// CodeError is any failure that is not a timeout.
	CodeError = "error"

	// CodeTimedOut is a run whose agent failed on a deadline of its own, such
	// as a model call's. A deadline the handler set is a cancel, not this.
	CodeTimedOut = "timed_out"
)

// WithCode sets RUN_ERROR's code.
func (e Event) WithCode(code string) Event {
	e.Code = code
	return e
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
//
// A call that did not succeed carries its outcome twice, at the top level and in
// metadata, because clients on either side of @ag-ui/client 1.0 read only one
// of the two -- see ToolOutcome.
func ToolCallResult(messageID, toolCallID, content string, outcome ToolOutcome) Event {
	event := Event{
		Type: EventToolCallResult, MessageID: messageID,
		ToolCallID: toolCallID, Content: content, Role: "tool",
		Outcome: outcome,
	}
	if outcome != OutcomeSuccess {
		event.Metadata = map[string]any{"outcome": string(outcome)}
	}
	return event
}

// PatchOperation is one RFC 6902 operation, as STATE_DELTA and ACTIVITY_DELTA
// carry them.
//
// Value is omitted when nil, which is right for remove, move and copy and wrong
// for add, replace and test: to write a null there, pass json.RawMessage("null").
type PatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	From  string `json:"from,omitempty"`
	Value any    `json:"value,omitempty"`
}

// Interrupt asks the client for a decision before the run can go on. ID and
// Reason are required; ToolCallID ties it to the call it concerns.
type Interrupt struct {
	ID             string `json:"id"`
	Reason         string `json:"reason"`
	Message        string `json:"message,omitempty"`
	ToolCallID     string `json:"toolCallId,omitempty"`
	ResponseSchema any    `json:"responseSchema,omitempty"`
	ExpiresAt      string `json:"expiresAt,omitempty"`
	Metadata       any    `json:"metadata,omitempty"`
}

// RunOutcome is how a run ended: "success", possibly with tool calls the client
// still has to answer, "interrupt", or "cancelled". Build one with Success,
// Interrupted or Cancelled and hand it to Stream.Finish.
type RunOutcome struct {
	Type               string      `json:"type"`
	PendingToolCallIDs []string    `json:"pendingToolCallIds,omitempty"`
	Interrupts         []Interrupt `json:"interrupts,omitempty"`
}

// Success is a run that completed. The ids name tool calls the agent made that
// the client, not the server, has to run: the run ends so the client can answer,
// and the next run carries the results.
func Success(pendingToolCallIDs ...string) RunOutcome {
	return RunOutcome{Type: "success", PendingToolCallIDs: pendingToolCallIDs}
}

// Interrupted is a run that stopped to ask for a decision. At least one
// interrupt is required, and each needs an id and a reason.
func Interrupted(interrupts ...Interrupt) RunOutcome {
	return RunOutcome{Type: "interrupt", Interrupts: interrupts}
}

// Cancelled is a run stopped before it completed that did not fail: nothing is
// produced and nothing is waited for. A handler sends it only to a client that
// declared a protocolVersion, as an older client's schema rejects the frame.
func Cancelled() RunOutcome {
	return RunOutcome{Type: "cancelled"}
}

// TokenUsage is one provider and model's token counts, as AG-UI 1.0 defines
// them: input and output are totals, reasoning and cache counts are parts of
// them. A zero count is "not reported" and is omitted, as pydantic-ai does.
type TokenUsage struct {
	Provider              string `json:"provider,omitempty"`
	Model                 string `json:"model,omitempty"`
	InputTokens           int64  `json:"inputTokens,omitempty"`
	OutputTokens          int64  `json:"outputTokens,omitempty"`
	TotalTokens           int64  `json:"totalTokens,omitempty"`
	ReasoningTokens       int64  `json:"reasoningTokens,omitempty"`
	CachedInputTokens     int64  `json:"cachedInputTokens,omitempty"`
	CacheWriteInputTokens int64  `json:"cacheWriteInputTokens,omitempty"`
}

// WithUsage attaches token usage to a RUN_FINISHED.
func (e Event) WithUsage(usage []TokenUsage) Event {
	e.Usage = usage
	return e
}

// RunFinishedWith closes a run with an outcome. A handler writes it for the
// outcome an agent set with Stream.Finish.
func RunFinishedWith(threadID, runID string, outcome RunOutcome) Event {
	return Event{
		Type: EventRunFinished, ThreadID: threadID, RunID: runID,
		payload: outcome, hasPayload: true,
	}
}

// StateSnapshot replaces the client's shared state with snapshot.
func StateSnapshot(snapshot any) Event {
	return Event{Type: EventStateSnapshot, payload: snapshot, hasPayload: true}
}

// StateDelta patches the client's shared state.
func StateDelta(ops ...PatchOperation) Event {
	return Event{Type: EventStateDelta, payload: nonNil(ops), hasPayload: true}
}

// MessagesSnapshot replaces the client's thread with messages.
func MessagesSnapshot(messages []Message) Event {
	return Event{Type: EventMessagesSnapshot, payload: nonNil(messages), hasPayload: true}
}

// ReasoningStart opens a reasoning span.
func ReasoningStart(messageID string) Event {
	return Event{Type: EventReasoningStart, MessageID: messageID}
}

// ReasoningMessageStart opens the reasoning message inside a span.
func ReasoningMessageStart(messageID string) Event {
	return Event{Type: EventReasoningMessageStart, MessageID: messageID, Role: "reasoning"}
}

// ReasoningMessageContent appends to the open reasoning message.
func ReasoningMessageContent(messageID, delta string) Event {
	return Event{Type: EventReasoningMessageContent, MessageID: messageID, Delta: delta}
}

// ReasoningMessageEnd closes the reasoning message.
func ReasoningMessageEnd(messageID string) Event {
	return Event{Type: EventReasoningMessageEnd, MessageID: messageID}
}

// ReasoningEnd closes the reasoning span.
func ReasoningEnd(messageID string) Event {
	return Event{Type: EventReasoningEnd, MessageID: messageID}
}

// ReasoningEncryptedValue carries a provider's opaque handle for a "message" or
// a "tool-call", which the client sends back so the provider can verify it.
func ReasoningEncryptedValue(subtype, entityID, encryptedValue string) Event {
	return Event{
		Type: EventReasoningEncryptedValue, Subtype: subtype,
		EntityID: entityID, EncryptedValue: encryptedValue,
	}
}

// ActivitySnapshot sets an activity message's object content, which the client
// renders by activityType. Without WithReplace the client replaces.
func ActivitySnapshot(messageID, activityType string, content any) Event {
	return Event{
		Type: EventActivitySnapshot, MessageID: messageID, ActivityType: activityType,
		payload: content, hasPayload: true,
	}
}

// ActivityDelta patches an activity message's content.
func ActivityDelta(messageID, activityType string, patch ...PatchOperation) Event {
	return Event{
		Type: EventActivityDelta, MessageID: messageID, ActivityType: activityType,
		payload: nonNil(patch), hasPayload: true,
	}
}

// WithReplace says whether an ACTIVITY_SNAPSHOT replaces the message's content
// or merges into it, where the protocol's default is to replace.
func (e Event) WithReplace(replace bool) Event {
	e.Replace = &replace
	return e
}

// Custom is an application event: a name and any value, which the client hands
// to whatever listens for that name.
func Custom(name string, value any) Event {
	return Event{Type: EventCustom, Name: name, payload: value, hasPayload: true}
}

// nonNil keeps an empty list an array on the wire, where a nil slice would be
// null and the schema requires an array.
func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}
