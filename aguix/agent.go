package aguix

import (
	"context"
	"encoding/json"
	"strings"
)

// Message is one entry in the conversation, as @ag-ui/core 1.0 declares it.
//
// The protocol's message is a union discriminated by role, and every role's
// fields are here as one struct for the reason Event is one struct: a union in
// Go would be a conversion layer that could disagree with the wire. A field a
// role does not declare is simply absent. Role is kept as sent, including a role
// this package has never heard of, so a newer client's history decodes rather
// than failing the run.
//
// Content is raw JSON because its type depends on the role: a string for most,
// an array of parts for a user or tool message that carries an image beside its
// text, and an object for an activity. Text reads it as the client does.
type Message struct {
	ID   string `json:"id"`
	Role string `json:"role"`

	// Content is absent on an assistant turn that only called tools.
	Content json.RawMessage `json:"content,omitempty"`
	Name    string          `json:"name,omitempty"`

	// ToolCalls are the calls an assistant message made, browser tools among
	// them. The answer to each comes back as a later message with role "tool"
	// whose ToolCallID names it, which is how a call is paired with its result
	// across runs.
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`

	// ToolCallID and Error are a tool message's: the call it answers, and why
	// the call failed when it did.
	ToolCallID string `json:"toolCallId,omitempty"`
	Error      string `json:"error,omitempty"`

	// ActivityType names what an activity message's object content is.
	ActivityType string `json:"activityType,omitempty"`

	// EncryptedValue is a provider's opaque handle, such as the signature of a
	// reasoning span, carried back so the provider can verify its own output.
	EncryptedValue string `json:"encryptedValue,omitempty"`

	// Metadata is the protocol's open slot, kept raw: its keys are the
	// producer's, and decoding them would round every number past 2^53.
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// Text returns the message's content as text, the way @ag-ui/core's
// contentToText reads it: a string as it is, the text parts of an array joined
// with nothing between them, and an empty string for anything else -- an
// activity's object, an absent content, or bytes that are not JSON at all.
func (m Message) Text() string {
	if len(m.Content) == 0 {
		return ""
	}
	switch m.Content[0] {
	case '"':
		var text string
		if err := json.Unmarshal(m.Content, &text); err != nil {
			return ""
		}
		return text
	case '[':
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(m.Content, &parts); err != nil {
			return ""
		}
		var b strings.Builder
		for _, part := range parts {
			if part.Type == "text" {
				b.WriteString(part.Text)
			}
		}
		return b.String()
	}
	return ""
}

// TextContent encodes text as a message's content, for a Message built in Go.
func TextContent(text string) json.RawMessage {
	raw, _ := json.Marshal(text) // a string always encodes
	return raw
}

// ToolCall is one call an assistant message made.
type ToolCall struct {
	ID string `json:"id"`

	// Type is "function", the only kind the protocol declares.
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`

	EncryptedValue string          `json:"encryptedValue,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}

// FunctionCall names the tool a call ran and what it was given.
//
// Arguments is JSON text, not a JSON object: the protocol carries it as a
// string, because a model produces it a token at a time.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ContextItem is a named piece of ambient information the client gives the
// agent for one run, beside the conversation rather than in it.
type ContextItem struct {
	Description string `json:"description"`
	Value       string `json:"value"`
}

// ResumeEntry answers one interrupt the previous run ended on.
//
// Status is "resolved" or "cancelled" as the protocol declares it today, and is
// kept as sent. Payload is the answer itself, raw, because its shape is what the
// interrupt asked for.
type ResumeEntry struct {
	InterruptID string          `json:"interruptId"`
	Status      string          `json:"status"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
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

	// Context is what the client says the agent should know for this run.
	Context []ContextItem `json:"context"`

	// Resume answers the interrupts the previous run ended on, one entry each.
	// Absent on a run that does not continue one.
	Resume []ResumeEntry `json:"resume,omitempty"`
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
