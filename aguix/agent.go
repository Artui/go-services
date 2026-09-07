package aguix

import (
	"context"
	"encoding/json"
)

// Message is one turn already in the conversation.
//
// The protocol's message union is wider than this -- developer and system
// roles, tool results, attachments, encrypted values. What is decoded here is
// what an agent needs to answer: who said it and what they said. A field added
// to the protocol does not break this, because unknown keys are ignored.
type Message struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

// ToolDefinition is a tool the CLIENT offers, sent with every run.
//
// The web component registers browser-side tools -- filling a field, clicking
// a control, drawing a chart -- and forwards them here so an agent can choose
// one. An agent that calls one of these emits the tool call and stops: the
// answer comes back on the next run, as a message, because the work happens in
// the page rather than on the server.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`

	// OutputSchema is what the operation answers with, when the spec declared
	// it. Absent otherwise, and absent on every tool a client sends inbound.
	//
	// AG-UI's own Tool carries a name, a description and parameters, and
	// nothing else -- so this is an addition rather than a field of the
	// protocol. It is spelt the way MCP spells it, because that is the
	// vocabulary a model has already been trained against and inventing a
	// second name for the same thing helps nobody. The protocol's models allow
	// unknown keys, so a client that has never heard of it parses the tool and
	// ignores the field rather than rejecting the run.
	//
	// It is here because the reader is the model, not the browser. mcpx and
	// adkx both advertise this from the same `services.Entry`, and without it
	// an AG-UI model is the only one of the three told what an operation takes
	// and not what it returns -- so an enum's values, a field's units, and
	// anything else the schema carries stop at this adapter.
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

// RunInput is one request from a client.
//
// It is the protocol's RunAgentInput, decoded down to the parts a server has to
// read. State is left as raw JSON: it is the client's own shape, this package
// has no opinion about it, and decoding it into a map would round every number
// through float64 for an agent that may only be passing it back.
type RunInput struct {
	ThreadID string           `json:"threadId"`
	RunID    string           `json:"runId"`
	Messages []Message        `json:"messages"`
	Tools    []ToolDefinition `json:"tools"`
	State    json.RawMessage  `json:"state"`

	// ForwardedProps is where the client puts anything the protocol does not
	// name. The web component sends its own context here.
	ForwardedProps json.RawMessage `json:"forwardedProps"`
}

// LastUserMessage returns the message the run is answering, and whether there
// was one.
//
// A run with no user message is not malformed -- a client may open a thread to
// let the agent speak first -- so this reports the absence rather than
// inventing an empty message for an agent to answer.
func (in RunInput) LastUserMessage() (Message, bool) {
	for i := len(in.Messages) - 1; i >= 0; i-- {
		if in.Messages[i].Role == "user" {
			return in.Messages[i], true
		}
	}
	return Message{}, false
}

// Agent runs one turn.
//
// It is handed the decoded input and a stream, and everything it emits reaches
// the client as it is written. The run's own lifecycle is not its business: the
// handler emits RUN_STARTED before this is called and RUN_FINISHED or RUN_ERROR
// after it returns, so an agent cannot leave a run half-open by returning early.
//
// Returning an error ends the run as a RUN_ERROR. The error's own words do not
// reach the client unless the handler is told they may -- see Handler.
type Agent interface {
	Run(ctx context.Context, in RunInput, out *Stream) error
}

// AgentFunc adapts a function to Agent.
type AgentFunc func(ctx context.Context, in RunInput, out *Stream) error

// Run calls f.
func (f AgentFunc) Run(ctx context.Context, in RunInput, out *Stream) error {
	return f(ctx, in, out)
}
