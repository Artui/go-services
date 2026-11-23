package aguiadk

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

// errModelReported is what a run fails with when the model answered with an
// error code. It carries the code and never the provider's message: aguix's
// handler redacts it anyway, and an operator's log is the place for the words.
var errModelReported = errors.New("aguiadk: the model reported an error")

// translator turns one run's ADK events into AG-UI frames.
//
// It is stateful because the two protocols cut a turn differently. Under SSE,
// ADK yields the model's text as partial events and then repeats all of it in
// one complete event, which is the one it stores and acts on; AG-UI wants the
// text once, as deltas inside a START/END pair. So text is taken from partials
// when there were any, and from the complete event only when there were none.
// Function calls are taken from complete events only, because that is where
// ADK executes them and where it assigns the ids its responses will carry.
type translator struct {
	newID func() string

	// open is the text message being streamed, and last is the assistant message
	// of the current step: the parent a tool call is announced under. A tool
	// result ends the step, so it is cleared there.
	open, last string

	// streamed records text deltas from partials since the last complete
	// event, so that event's aggregate of the same text is not sent again.
	streamed bool

	waiting []string

	// approval names the prompts of gated specs, and denied the calls this run
	// resumed with a denial. interrupts are the confirmations the run asked for.
	approval   *Approval
	denied     map[string]bool
	interrupts []aguix.Interrupt
}

func newTranslator(newID func() string) *translator {
	return &translator{newID: newID}
}

// translate returns the frames one event produces.
//
// An error leaves the translator exactly as it was, so the caller can still
// close() whatever the earlier events opened before it fails the run.
func (t *translator) translate(ev *session.Event) ([]aguix.Event, error) {
	if ev.ErrorCode != "" || ev.ErrorMessage != "" {
		return nil, fmt.Errorf("%w (code %q)", errModelReported, ev.ErrorCode)
	}
	if ev.Author == userAuthor {
		return nil, nil
	}
	var parts []*genai.Part
	if ev.Content != nil {
		parts = ev.Content.Parts
	}

	if ev.Partial {
		var out []aguix.Event
		for _, p := range parts {
			if p != nil && !p.Thought && p.Text != "" {
				out = t.text(out, p.Text)
				t.streamed = true
			}
		}
		return out, nil
	}

	encoded, err := encodeParts(parts)
	if err != nil {
		return nil, err
	}
	var interrupts []aguix.Interrupt
	for _, p := range parts {
		if confirmation(p, ev) {
			in, err := t.approval.interruptFor(p.FunctionCall)
			if err != nil {
				return nil, err
			}
			interrupts = append(interrupts, in)
		}
	}
	t.interrupts = append(t.interrupts, interrupts...)
	// A gated call is answered by a placeholder until the person decides; the
	// client is told nothing, so the call's card waits for the real result.
	awaiting := ev.Actions.RequestedToolConfirmations

	streamed := t.streamed
	t.streamed = false
	var out []aguix.Event
	for i, p := range parts {
		switch {
		// Thought parts are reasoning, which this bridge does not stream yet.
		case p == nil, p.Thought:
		case confirmation(p, ev):
		case p.FunctionResponse != nil && hasKey(awaiting, p.FunctionResponse.ID):
		case p.FunctionCall != nil:
			out = t.call(out, p.FunctionCall, encoded[i], ev.LongRunningToolIDs)
		case p.FunctionResponse != nil:
			out = t.result(out, p.FunctionResponse, encoded[i])
		case p.Text != "" && !streamed:
			out = t.text(out, p.Text)
		}
	}
	return t.endText(out), nil
}

// close ends whatever is still open when the run stops.
func (t *translator) close() []aguix.Event {
	return t.endText(nil)
}

// pending names the long-running calls seen and not yet answered, in order.
func (t *translator) pending() []string {
	return slices.Clone(t.waiting)
}

func (t *translator) text(out []aguix.Event, delta string) []aguix.Event {
	if t.open == "" {
		t.open = t.newID()
		t.last = t.open
		out = append(out, aguix.TextMessageStart(t.open))
	}
	return append(out, aguix.TextMessageContent(t.open, delta))
}

func (t *translator) endText(out []aguix.Event) []aguix.Event {
	if t.open == "" {
		return out
	}
	out = append(out, aguix.TextMessageEnd(t.open))
	t.open = ""
	return out
}

func (t *translator) call(
	out []aguix.Event, fc *genai.FunctionCall, args json.RawMessage, longRunning []string,
) []aguix.Event {
	out = t.endText(out)
	out = append(out,
		aguix.ToolCallStart(fc.ID, fc.Name, t.last),
		aguix.ToolCallArgs(fc.ID, args),
		aguix.ToolCallEnd(fc.ID),
	)
	if slices.Contains(longRunning, fc.ID) && !slices.Contains(t.waiting, fc.ID) {
		t.waiting = append(t.waiting, fc.ID)
	}
	return out
}

// result reports a tool's answer. Every ADK and adkx error site writes a failed
// call as the response {"error": text} and nothing else (base_flow.go's tool
// paths, adkx's refuse); adkx has already redacted the text by then. A response
// with other keys is a success, even one whose output has an "error" field.
func (t *translator) result(
	out []aguix.Event, fr *genai.FunctionResponse, content json.RawMessage,
) []aguix.Event {
	outcome := aguix.OutcomeSuccess
	switch {
	case t.denied[fr.ID]:
		outcome = aguix.OutcomeDenied
	case failed(fr.Response):
		outcome = aguix.OutcomeFailed
	}
	out = t.endText(out)
	out = append(out, aguix.ToolCallResult(t.newID(), fr.ID, string(content), outcome))
	t.waiting = slices.DeleteFunc(t.waiting, func(id string) bool { return id == fr.ID })
	t.last = ""
	return out
}

// confirmation reports whether p is ADK's own request to confirm a call. It is
// long-running, which a model's call to a tool of that name is not.
func confirmation(p *genai.Part, ev *session.Event) bool {
	return p != nil && p.FunctionCall != nil && p.FunctionCall.Name == toolconfirmation.FunctionCallName &&
		slices.Contains(ev.LongRunningToolIDs, p.FunctionCall.ID)
}

func hasKey[V any](m map[string]V, key string) bool {
	_, ok := m[key]
	return ok
}

func failed(response map[string]any) bool {
	_, isText := response["error"].(string)
	return isText && len(response) == 1
}

// encodeParts encodes every call's arguments and every response up front, so a
// failure is found before any state changes. A nil map is sent as "{}".
func encodeParts(parts []*genai.Part) (map[int]json.RawMessage, error) {
	encoded := make(map[int]json.RawMessage)
	for i, p := range parts {
		var (
			value map[string]any
			what  string
		)
		switch {
		case p == nil:
			continue
		case p.FunctionCall != nil:
			value, what = p.FunctionCall.Args, "the arguments of a call to "+p.FunctionCall.Name
		case p.FunctionResponse != nil:
			value, what = p.FunctionResponse.Response, "the response of "+p.FunctionResponse.Name
		default:
			continue
		}
		if value == nil {
			value = map[string]any{}
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("aguiadk: %s cannot be encoded: %w", what, err)
		}
		encoded[i] = raw
	}
	return encoded, nil
}
