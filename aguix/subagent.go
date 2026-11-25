package aguix

// SubagentStarted opens one sub-agent invocation. subagentRunID is an opaque
// handle for this invocation, not the sub-agent's name: two invocations of the
// same sub-agent carry two ids. parentToolCallID names the tool call that
// delegated to it, which is how the web component draws the sub-agent's
// progress inside that call's card; pass "" when no call did.
//
// Everything the sub-agent then produces is attributed to it by carrying the
// same id, which WithSubagentRunID sets.
func SubagentStarted(subagentRunID, name, parentToolCallID string) Event {
	return Event{
		Type: EventSubagentStarted, SubagentRunID: subagentRunID,
		Name: name, ParentToolCallID: parentToolCallID,
	}
}

// WithDescription sets a SUBAGENT_STARTED's description of the sub-agent.
func (e Event) WithDescription(description string) Event {
	e.Description = description
	return e
}

// WithParentSubagentRunID nests a SUBAGENT_STARTED inside the invocation that
// started it. On SUBAGENT_STARTED the subagentRunId identifies the new
// invocation rather than attributing the event, so nesting needs its own key.
func (e Event) WithParentSubagentRunID(parentSubagentRunID string) Event {
	e.ParentSubagentRunID = parentSubagentRunID
	return e
}

// WithSubagentRunID attributes an event to the sub-agent invocation that
// produced it. The protocol allows it on every message, tool call, reasoning,
// state, activity and custom event, and not on RUN_STARTED, RUN_FINISHED or
// RUN_ERROR, which belong to the run: a sub-agent that fails without ending
// the run reports SubagentError instead. The web component leaves an
// attributed tool call to the sub-agent's panel rather than running it.
func (e Event) WithSubagentRunID(subagentRunID string) Event {
	e.SubagentRunID = subagentRunID
	return e
}

// SubagentFinished ends an invocation's segment of the run because its work
// completed, which the protocol reads from the absent outcome.
func SubagentFinished(subagentRunID string) Event {
	return Event{Type: EventSubagentFinished, SubagentRunID: subagentRunID}
}

// SubagentSuspended ends an invocation's segment of the run because it waits
// on outside input. It is terminal for this stream, not for the invocation: a
// later run may carry the same id on once the named interrupts are answered.
func SubagentSuspended(subagentRunID string, interruptIDs ...string) Event {
	return Event{
		Type: EventSubagentFinished, SubagentRunID: subagentRunID,
		payload: subagentOutcome{Type: "suspended", InterruptIDs: interruptIDs}, hasPayload: true,
	}
}

type subagentOutcome struct {
	Type         string   `json:"type"`
	InterruptIDs []string `json:"interruptIds,omitempty"`
}

// WithResult sets what a SUBAGENT_FINISHED's invocation produced. A nil result
// writes no field; a value that encodes to null, such as a nil pointer, is
// refused by the client's parser, so pass nil rather than one.
func (e Event) WithResult(result any) Event {
	e.Result = result
	return e
}

// SubagentError reports that an invocation failed. The message is required by
// the client's schema and reaches the user, so redact it as RunError is. The
// run goes on: a parent agent may handle the failure, which is why this is not
// RUN_ERROR. WithCode sets its code.
func SubagentError(subagentRunID, message string) Event {
	return Event{Type: EventSubagentError, SubagentRunID: subagentRunID, Message: message}
}
