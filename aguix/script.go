package aguix

import (
	"context"
	"encoding/json"
	"strings"
)

// Caller is what a script needs from a toolbox: run one operation and stream
// it. *Toolbox[D] satisfies it for every D, which is why it exists -- a step
// has to be storable in a slice beside other steps, and a generic type cannot
// be.
type Caller interface {
	Call(ctx context.Context, out *Stream, parentMessageID, name string, args json.RawMessage) error
}

// Step is one thing a scripted agent does.
type Step func(ctx context.Context, in RunInput, out *Stream) error

// Say streams text as an assistant message, one word at a time.
//
// Word by word rather than all at once, because the point of a scripted agent
// is to exercise the transport the way a real one does: a client that only ever
// receives a whole message in a single TEXT_MESSAGE_CONTENT has not been shown
// to reassemble deltas, and reassembling deltas is most of what the client
// does.
func Say(text string) Step {
	return func(_ context.Context, _ RunInput, out *Stream) error {
		messageID := messageIDs()
		if err := out.Emit(TextMessageStart(messageID)); err != nil {
			return err
		}
		for i, word := range strings.Fields(text) {
			delta := word
			if i > 0 {
				delta = " " + word
			}
			if err := out.Emit(TextMessageContent(messageID, delta)); err != nil {
				return err
			}
		}
		return out.Emit(TextMessageEnd(messageID))
	}
}

// CallTool runs one registry operation and streams the exchange.
//
// The arguments are fixed, which is the whole idea: a scripted agent is a model
// whose decisions are already made, so a run is reproducible and a test can
// assert the transcript rather than a shape.
func CallTool(caller Caller, name string, args json.RawMessage) Step {
	return func(ctx context.Context, _ RunInput, out *Stream) error {
		return caller.Call(ctx, out, "", name, args)
	}
}

// CallToolFrom is CallTool with the arguments built from the run.
//
// It is what makes a scripted agent answer the user rather than recite: a rule
// that matches "borrow book 4" can read the 4 out of the message. The function
// returning an error ends the run.
func CallToolFrom(
	caller Caller, name string, args func(RunInput) (json.RawMessage, error),
) Step {
	return func(ctx context.Context, in RunInput, out *Stream) error {
		built, err := args(in)
		if err != nil {
			return err
		}
		return caller.Call(ctx, out, "", name, built)
	}
}

// Rule is one branch of a script: when the run matches, run these steps.
type Rule struct {
	// When decides whether this rule answers the run. A nil When always
	// matches, which is how a fallback is written.
	When func(RunInput) bool

	// Steps run in order. The first error ends the run.
	Steps []Step
}

// WhenUserSays matches when the last user message contains every one of these,
// case-insensitively.
//
// Substring matching is not a pretence at understanding. It is the smallest
// thing that lets a demo respond to what was typed, and being obviously crude
// is a feature: nobody reading this will mistake it for a model.
func WhenUserSays(words ...string) func(RunInput) bool {
	return func(in RunInput) bool {
		message, ok := in.LastUserMessage()
		if !ok {
			return false
		}
		content := strings.ToLower(message.Content)
		for _, word := range words {
			if !strings.Contains(content, strings.ToLower(word)) {
				return false
			}
		}
		return true
	}
}

// Scripted is an agent that answers from a fixed set of rules.
//
// The first rule whose When matches wins, so order is precedence and a rule
// with no When is the fallback. A run that matches nothing says nothing and
// finishes cleanly, which is a legitimate outcome and not an error: the client
// shows an empty turn rather than a failure it cannot act on.
//
// This exists so an AG-UI endpoint can be exercised end to end without a model.
// Everything below it is the real thing -- the same stream, the same event
// order, the same registry, the same transaction boundary -- and the only
// pretend part is which tool gets called, which is exactly the part a test
// wants to hold still.
func Scripted(rules ...Rule) Agent {
	return AgentFunc(func(ctx context.Context, in RunInput, out *Stream) error {
		for _, rule := range rules {
			if rule.When != nil && !rule.When(in) {
				continue
			}
			for _, step := range rule.Steps {
				if err := step(ctx, in, out); err != nil {
					return err
				}
			}
			return nil
		}
		return nil
	})
}

// messageIDs numbers assistant messages across the process, so a transcript is
// comparable between runs.
var messageIDs = sequentialIDs("msg")

// A note on what a script cannot do, because it is easy to write a demo that
// pretends otherwise.
//
// Steps run in order and none of them sees what the previous one produced. A
// Say after a CallTool is written before the call has happened, so a script
// that closes with "that is done" says it over a refusal just as readily as
// over a success. The first demo written against this package did exactly that:
// the tool result read "no copy is on the shelf" and the assistant congratulated
// itself directly underneath.
//
// That is not a gap to be filled here. A real agent gets the tool result back as
// a message and decides what to say next, which is a second turn -- and giving a
// script the same ability would make it a small agent framework, competing with
// the real ones this package is meant to serve. What a script should do instead
// is let the result speak: emit the call, and say nothing about how it went.
