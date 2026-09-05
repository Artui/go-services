package aguix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	services "github.com/Artui/go-services"
)

// ToolErrorPrefix marks a tool result as a failure, in the content itself.
//
// AG-UI's TOOL_CALL_RESULT carries a content string, and as published it
// carried nothing else: no field said whether the call succeeded, and the web
// component settles every result it receives as done. So a refusal that simply
// put its reason in the content was indistinguishable from a success -- the
// card read "done" with "no copy is on the shelf" folded inside it, which is a
// failure disguised as an outcome.
//
// There is a structural answer now as well: Event.Outcome, serialised as
// "outcome" and emitted on every result that did not succeed. The two are not
// alternatives, and dropping either would lose something a reader has.
//
//   - The field is for a client that reads it. It is machine-readable, cannot
//     be mistaken for a service's own words, and survives the content being
//     re-rendered, translated or truncated.
//   - The prefix is for the model, and for every client that does not read the
//     field. A tool result is fed back to whatever decides the next move, and
//     that reader is handed the content rather than the envelope around it. The
//     prefix is also the convention the component already emits when its own
//     browser-side tool handler throws, so the model sees one shape whichever
//     side the tool ran on, and a person reading the transcript sees the word
//     before the reason rather than after it.
//
// Where this stands, stated so nobody has to infer it: the field is proposed
// upstream and emitted ahead of standardisation. A client that has not adopted
// it sees exactly the stream it saw before -- see ToolOutcome for why that is
// checked rather than assumed -- and that includes the card still reading
// "done", which remains the component's to fix and is why the prefix is not a
// stopgap to be dropped once the field is standard.
const ToolErrorPrefix = "Error: "

// ToolResultError is what a tool result says when a call failed for a reason
// outside the kernel's taxonomy.
//
// A tool result is rendered in the chat and, on an agentic page, read back by
// whatever decides the next move. So it gets the same treatment RUN_ERROR does:
// the taxonomy's own words, which a spec author wrote for a caller, and a fixed
// sentence for everything else.
const ToolResultError = ToolErrorPrefix +
	"The operation failed. The reason was recorded on the server."

// Principal turns a run's context into whatever the registry's resolver
// expects. Identity arrives the way it does in any net/http server -- put there
// by middleware -- because this package has no channel of its own for it and
// inventing one would compete with the one the application already has.
type Principal func(context.Context) (any, error)

// Anonymous authenticates nobody. Spelled out at the call site so that an
// unauthenticated agent is a choice rather than an omission.
func Anonymous(context.Context) (any, error) { return nil, nil }

// Toolbox lets an agent call the operations in a registry and streams the whole
// exchange as AG-UI events.
//
// This is the seam between the two halves of the repository. Everything below
// Dispatch -- validation, the permission layer, the transaction boundary, the
// error taxonomy -- is the same code the HTTP and MCP adapters run, and nothing
// about it is restated here. What this adds is the streaming: a client watching
// the run sees the call announced, its arguments, and its answer, in order.
type Toolbox[D any] struct {
	reg       *services.Registry[D]
	principal Principal
	ids       func() string
}

// NewToolbox builds one over a registry.
func NewToolbox[D any](reg *services.Registry[D], principal Principal) (*Toolbox[D], error) {
	if reg == nil {
		return nil, errors.New("aguix: a toolbox needs a registry")
	}
	if principal == nil {
		return nil, errors.New(
			"aguix: a toolbox needs a principal; pass aguix.Anonymous to authenticate nobody")
	}
	return &Toolbox[D]{reg: reg, principal: principal, ids: sequentialIDs("call")}, nil
}

// Definitions describes every operation, in the shape a model is shown.
//
// The parameters are the schema the kernel reflected at registration, so what
// an agent is told about an operation and what the kernel enforces are the same
// object -- the same property mcpx and adkx rest on.
func (t *Toolbox[D]) Definitions() ([]ToolDefinition, error) {
	entries := t.reg.Entries()
	defs := make([]ToolDefinition, 0, len(entries))
	for _, entry := range entries {
		parameters, err := json.Marshal(entry.Input)
		if err != nil {
			return nil, fmt.Errorf("aguix: %q has an input schema that cannot be encoded: %w",
				entry.Name, err)
		}
		defs = append(defs, ToolDefinition{
			Name:        entry.Name,
			Description: entry.Description,
			Parameters:  parameters,
		})
	}
	return defs, nil
}

// Call dispatches one operation and streams it.
//
// The event order is the protocol's: the call is announced, its arguments
// follow, the argument stream is closed, and then the result. A client that
// renders a call before its arguments have arrived shows an empty card, which
// is why the result is never emitted before TOOL_CALL_END.
//
// A refused call is a result, not a failed run. The agent asked for something
// it was not allowed to have; the conversation continues, and the model or the
// user decides what to do about it. Returning an error here would end the run
// and lose the answer.
func (t *Toolbox[D]) Call(
	ctx context.Context, out *Stream, parentMessageID, name string, args json.RawMessage,
) error {
	callID := t.ids()

	if err := out.Emit(ToolCallStart(callID, name, parentMessageID)); err != nil {
		return err
	}
	// An absent argument object is sent as "{}" rather than omitted: the field
	// is a string being reassembled by the client, and an empty one leaves it
	// with nothing to parse.
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if err := out.Emit(ToolCallArgs(callID, args)); err != nil {
		return err
	}
	if err := out.Emit(ToolCallEnd(callID)); err != nil {
		return err
	}

	content, outcome, runErr := t.dispatch(ctx, name, args)
	if err := out.Emit(ToolCallResult(t.ids(), callID, content, outcome)); err != nil {
		return err
	}
	return runErr
}

// dispatch runs the operation, renders its answer as the string a tool result
// carries, and says how it ended.
//
// The third return is the error the RUN should fail with, which is almost never
// set: a refusal and a validation failure are answers, and only a fault in this
// deployment is a reason to stop the run.
func (t *Toolbox[D]) dispatch(
	ctx context.Context, name string, args json.RawMessage,
) (string, ToolOutcome, error) {
	who, err := t.principal(ctx)
	if err != nil {
		content, outcome := t.explain(err)
		return content, outcome, nil
	}

	result, err := t.reg.Dispatch(ctx, who, name, args)
	if err != nil {
		content, outcome := t.explain(err)
		return content, outcome, nil
	}
	// Only a success reaches the encoder below, so only a success is rendered
	// as bare JSON and only a success carries no outcome. Everything explain
	// returns is both prefixed and marked, and the two markers are set in the
	// same place so they cannot come to disagree.

	payload, err := json.Marshal(result.Value)
	if err != nil {
		// The service returned something no encoder can represent. That is this
		// process's bug rather than anything the agent did, and it is the one
		// case worth ending the run over: a client shown a tool result it
		// cannot parse has no way to tell that from an empty answer. The result
		// still goes out, marked failed, so the transcript is not a call with
		// no answer.
		return ToolResultError, OutcomeFailed,
			fmt.Errorf("aguix: %q returned an unencodable value: %w", name, err)
	}
	return string(payload), OutcomeSuccess, nil
}

// explain is the taxonomy split, in the wording a tool result uses and in the
// outcome it reports.
//
// A validation failure is rendered as prose rather than as a JSON error
// envelope, which is what the HTTP adapters send. The reader is different: a
// chat client shows this text and an agentic page reads it back to decide what
// to do next, so the answer says which argument was wrong and that trying again
// is the expected move. It is the same call mcpx and adkx make, for the same
// reason, and it is why the wording is theirs rather than the HTTP one.
//
// The outcome is decided here too, from the same error, so the prefix and the
// field can never contradict each other: nothing returns one without the other.
func (t *Toolbox[D]) explain(err error) (string, ToolOutcome) {
	var invalid *services.ValidationError
	switch {
	// Failed, not denied. The call was attempted, the arguments were read, and
	// they were rejected on their merits -- and the answer says how to fix
	// them, so a caller that rephrases gets a different result. That is the
	// opposite of a denial.
	//
	// The nil check is not redundant with errors.As: a helper returning
	// *ValidationError assigned into an error yields a non-nil error holding a
	// nil pointer, which errors.As matches. Rendering that as "the arguments
	// were rejected" with nothing listed invites a retry that cannot succeed.
	case errors.As(err, &invalid) && invalid != nil:
		return ToolErrorPrefix + explainValidation(invalid), OutcomeFailed

	// Denied, and this is the mapping worth arguing rather than assuming. The
	// contract's "denied" means refused by a person or a guard rather than
	// attempted, and that is what this sentinel says in the kernel's own words:
	// the acting principal may not do this. Nothing happened -- the refusal
	// came from the principal before the registry was reached, or from the deps
	// resolver before the service ran, or from a guard the spec author wrote,
	// in which case the transaction rolled back -- and no rephrasing of the
	// arguments changes the answer, which is exactly what separates it from the
	// two below.
	//
	// Where the refusal came from is deliberately not distinguished. To a
	// client the three mean one thing, and a wire field is worth having only
	// while every producer fills it the same way.
	case errors.Is(err, services.ErrPermission):
		return ToolErrorPrefix + err.Error(), OutcomeDenied

	// Failed. These two are answers about the state of the world rather than
	// about the caller: the call ran, looked, and found the row missing or the
	// current state forbidding it. A client can offer "sign in as someone else"
	// for the denial above and "ask for a different book" for these, which is
	// the whole reason the distinction is on the wire at all.
	case errors.Is(err, services.ErrNotFound),
		errors.Is(err, services.ErrConflict):
		return ToolErrorPrefix + err.Error(), OutcomeFailed
	}
	// Outside the taxonomy: a bug, or an upstream that broke. Failed rather
	// than denied, because nobody refused anything -- and reporting a fault as
	// a denial would tell a client to stop asking, which is the one conclusion
	// it must not draw from a server that is simply broken.
	return ToolResultError, OutcomeFailed
}

// explainValidation lists the rejected arguments, one per line.
//
// Sorted, because Go randomises map iteration and a client shown two orderings
// of one failure has been shown two failures. The kernel's non-field key is
// printed without its key: it names the payload as a whole, and showing
// "non_field_errors" beside real argument names invites a reader to go looking
// for an argument by that name.
func explainValidation(e *services.ValidationError) string {
	fields := e.FieldMap()
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("The arguments were rejected. Correct these and try again:")
	for _, name := range names {
		for _, message := range fields[name] {
			b.WriteString("\n- ")
			if name != services.NonFieldKey {
				b.WriteString(name)
				b.WriteString(": ")
			}
			b.WriteString(message)
		}
	}
	return b.String()
}
