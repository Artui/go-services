package mcpx

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/Artui/go-services"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// InternalErrorText is what a tool result says when the call failed for a
// reason outside the kernel's error taxonomy.
//
// The words of an unexpected error are written for an operator: they name
// tables, hosts and internal state, and a model shown them will either repeat
// them to a user or try to act on them. So the wire gets a fixed sentence and
// the real error goes to the mount's ErrorReporter instead.
//
// It is exported so a consumer can assert on it without matching prose.
//
// The kernel exports a constant of the same name for the HTTP adapters. They
// are not duplicates and must not be merged: that one is a response body for a
// human or an operator reading a status page, this one is addressed to a model
// deciding what to do next, which is why it says the arguments were not the
// problem.
const InternalErrorText = "The service failed unexpectedly. " +
	"The reason was recorded on the server and is not available here. " +
	"The arguments were accepted, so changing them is unlikely to help."

// CancelledText and TimedOutText are what a tool result says when the call's
// own context ended before the service finished.
//
// They are a separate answer from InternalErrorText because they are a
// separate event. A cancelled call did not malfunction: usually the caller
// walked away, and on a busy mount that is ordinary traffic. Rendering it as an
// internal failure also hands it to the ErrorReporter, and a log where routine
// client timeouts outnumber real faults is a log nobody reads.
//
// Neither says anything about whether the work took effect. An atomic spec
// rolls back and a non-atomic one may not, and the mount cannot tell which from
// here -- so it does not guess.
const (
	CancelledText = "The call was cancelled before it completed."
	TimedOutText  = "The call ran out of time before it completed."
)

// succeed renders a dispatch that ran.
//
// Both halves of the result carry the same value: StructuredContent for a
// client that reads the output schema, and a JSON text block for one that does
// not. They cannot disagree, because they are one value encoded by one encoder
// -- the text is produced here rather than by summarising, precisely so that
// there is nothing to keep in step.
//
// Result.Status is deliberately dropped. It is an HTTP-ish hint the kernel
// carries for the transports that have status codes; MCP has none, and
// inventing a field to carry it would put an HTTP concept on this wire for no
// reader's benefit.
func succeed(res services.Result) (*mcp.CallToolResult, error) {
	payload, err := json.Marshal(res.Value)
	if err != nil {
		// Reached when a service returns something unencodable, which is a bug
		// in that service rather than anything the caller did. Returning it as
		// an error lets the caller take the same internal-error path as any
		// other unexpected failure, reporter included.
		return nil, err
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(payload)}},
		StructuredContent: res.Value,
	}, nil
}

// refuse renders a failure as a tool result rather than a protocol error.
//
// A service that declines to run has not malfunctioned: it has answered, and
// the answer is one the model has to read and react to. A JSON-RPC error would
// hide it behind the client's transport-failure path, where every domain
// refusal starts looking like an outage and none of them can be corrected.
//
// The second return reports whether the taxonomy recognised the error, so the
// caller can decide what to tell its reporter.
func refuse(ctx context.Context, err error) (*mcp.CallToolResult, bool) {
	text, known := explain(ctx, err)
	if !known {
		text = InternalErrorText
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}, known
}

// explain maps a kernel error onto the text a tool result carries, reporting
// whether the taxonomy named it.
//
// The taxonomy is matched with errors.Is and errors.As rather than by type
// equality, so a service's own wrapping survives: fmt.Errorf("%w: author %d is
// not yours", services.ErrPermission, id) is still a permission refusal, and
// its added words are exactly the ones the model needs.
//
// A recognised error's own text goes on the wire verbatim. That is safe in a
// way an unexpected error's is not, because these errors were written to be
// declined with -- the spec author chose the words knowing a caller reads them.
func explain(ctx context.Context, err error) (string, bool) {
	var invalid *services.ValidationError
	switch {
	// The nil check is not redundant with errors.As. A helper returning
	// *ValidationError assigned straight into an error yields a non-nil error
	// holding a nil pointer, and errors.As matches it -- so without this, a
	// server-side bug is rendered to a model as "the arguments were rejected"
	// with nothing listed, inviting it to retry a call that cannot succeed.
	// Classified as unexpected, it reaches the reporter instead, which is where
	// a bug of that shape belongs.
	case errors.As(err, &invalid) && invalid != nil:
		return explainValidation(invalid), true

	case errors.Is(err, services.ErrPermission),
		errors.Is(err, services.ErrNotFound),
		errors.Is(err, services.ErrConflict):
		return err.Error(), true

	// Interruption is checked last, so a refusal that happens to be returned
	// while the caller is going away is still reported as the refusal it is.
	//
	// Both halves of the condition are load-bearing. Matching the error alone
	// would also swallow a context error a service produced internally -- a
	// timed-out call to a third party, with this request's context still live
	// -- which is a real fault an operator needs to see. Requiring that this
	// call's own context is done is what separates "the caller left" from
	// "something inside took too long".
	case ctx.Err() != nil && errors.Is(err, context.Canceled):
		return CancelledText, true
	case ctx.Err() != nil && errors.Is(err, context.DeadlineExceeded):
		return TimedOutText, true
	}
	return "", false
}

// explainValidation renders per-field messages as something a model can act on.
//
// ValidationError.Error joins everything onto one line, which is right for a Go
// log and wrong here: the reader is deciding which argument to change, so the
// fields are listed one per line and the text says outright that retrying is
// the expected next move.
//
// The kernel's non-field key is rendered without its key. It names the payload
// as a whole rather than an argument, and printing "non_field_errors" beside
// real argument names invites a model to go looking for an argument by that
// name.
func explainValidation(e *services.ValidationError) string {
	// FieldMap rather than the Fields map directly. It is the kernel's nil-safe
	// accessor, and reading the struct field is what bypasses that safety: a nil
	// receiver dereferences here and takes the process down, because the SDK
	// recovers nowhere and this runs on a goroutine jsonrpc2 spawned. explain
	// already refuses to call this with a nil receiver; going through the
	// accessor means neither guarantee depends on the other holding.
	fields := e.FieldMap()

	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	// Sorted so two identical failures read identically. Go map order is not.
	sort.Strings(names)

	lines := make([]string, 0, len(names))
	for _, name := range names {
		for _, message := range fields[name] {
			if name == services.NonFieldKey {
				lines = append(lines, message)
				continue
			}
			lines = append(lines, name+": "+message)
		}
	}

	// The lines are counted rather than the fields, because a ValidationError
	// can be blamed on a field and still carry no message for it -- Invalid
	// takes its messages variadically. Either shape would otherwise produce a
	// heading with nothing under it, which reads to a model as a failure it is
	// expected to fix without being told what.
	if len(lines) == 0 {
		return "The arguments were rejected, with no reason given."
	}
	return "The arguments were rejected. Correct these and call the tool again:\n- " +
		strings.Join(lines, "\n- ")
}
