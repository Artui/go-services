package services

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The kernel's error taxonomy. Adapters map these to their own wire with
// errors.Is / errors.As, so a consumer's own wrapping survives the trip: the
// mapping table is per-adapter, the taxonomy is not.
var (
	// ErrNotFound reports that a named spec, or the row a service went looking
	// for, does not exist.
	ErrNotFound = errors.New("services: not found")

	// ErrConflict reports that the request was understood but the current state
	// forbids it -- the 409 case, distinct from a permission refusal.
	ErrConflict = errors.New("services: conflict")

	// ErrPermission reports that the acting principal may not do this.
	ErrPermission = errors.New("services: permission denied")

	// ErrConfiguration reports a fault in how an operation was mounted rather
	// than in the request that arrived.
	//
	// It is separate from ValidationError because the two address different
	// people. A validation failure is addressed to the caller and tells them
	// what to change; a configuration fault is addressed to whoever wrote the
	// route table, and no change the caller makes will help. Answering it as a
	// 400 puts an operator's diagnostic on the client's channel, beside genuine
	// client errors that an adapter would then be unable to tell it from.
	ErrConfiguration = errors.New("services: configuration error")

	// ErrBodyTooLarge reports that the client sent more than an adapter is
	// willing to read. It lives here rather than in each adapter because the
	// limit and the answer are one decision: two transports refusing at
	// different sizes is a difference a client cannot predict.
	ErrBodyTooLarge = errors.New("services: request body too large")
)

// DefaultMaxBodyBytes is the request-body ceiling an adapter applies unless it
// is configured otherwise.
//
// Unbounded reads are the default in most Go HTTP code and a denial-of-service
// waiting to happen. One mebibyte is generous for a JSON operation payload;
// anything transferring bulk data wants its own endpoint rather than a bigger
// number here.
const DefaultMaxBodyBytes int64 = 1 << 20

// The HTTP projection of the taxonomy above.
//
// These live here, in a package that imports no transport, for the same reason
// EncodeParams does: every HTTP-shaped adapter needs them and they are
// client-visible, so two adapters carrying their own copies is a difference a
// caller can observe. An adapter on a wire without status codes ignores them,
// as the MCP one does.
const (
	// StatusBodyTooLarge is the status for ErrBodyTooLarge, which StatusFor
	// returns like any other member of the taxonomy. What is not shared is the
	// recognition: an adapter maps its own transport's oversize error onto the
	// sentinel first, because that type belongs to the transport.
	StatusBodyTooLarge = 413

	// InternalErrorText is the body for an error outside the taxonomy. An
	// unexpected error's words are written for an operator, and putting them on
	// the wire is how internal detail reaches strangers.
	InternalErrorText = "internal server error"

	// UnreadableBodyText is the body for a request that could not be read to
	// the end -- a truncated upload rather than a malformed one.
	UnreadableBodyText = "the request body could not be read"

	// BodyTooLargeText is the body for a request over the size ceiling.
	BodyTooLargeText = "request body too large"
)

// StatusFor maps an error to the HTTP status an adapter should answer with.
//
// Anything outside the taxonomy is 500, which is the safe direction: an
// unrecognised error is a bug until proven otherwise, and answering 400 would
// tell a caller to fix a request that was never the problem.
func StatusFor(err error) int {
	var invalid *ValidationError
	switch {
	case errors.As(err, &invalid):
		return 400
	case errors.Is(err, ErrPermission):
		return 403
	case errors.Is(err, ErrNotFound):
		return 404
	case errors.Is(err, ErrConflict):
		return 409
	case errors.Is(err, ErrBodyTooLarge):
		return StatusBodyTooLarge
	case errors.Is(err, ErrConfiguration):
		// Listed rather than left to the default, because 500 is the right
		// answer here for a reason worth stating: the deployment is wrong, and
		// telling the caller to fix their request would be a lie.
		return 500
	default:
		return 500
	}
}

// FieldMap returns the per-field messages, never nil.
//
// Fields is an exported field on a constructible struct, so a
// &ValidationError{} with no map reaches a renderer sooner or later. Rendering
// that directly puts {"errors": null} on the wire, which a client parsing
// errors as an object cannot read. Adapters render through this so all of them
// answer the same shape.
func (e *ValidationError) FieldMap() map[string][]string {
	if e.Fields == nil {
		return map[string][]string{}
	}
	return e.Fields
}

// ValidationError carries per-field messages from any of the three validation
// layers. Fields is keyed by the JSON name, not the Go field name, because the
// key is going onto a wire the client speaks.
type ValidationError struct {
	Fields map[string][]string
}

func (e *ValidationError) Error() string {
	if len(e.Fields) == 0 {
		return "services: validation failed"
	}
	names := make([]string, 0, len(e.Fields))
	for name := range e.Fields {
		names = append(names, name)
	}
	// Sorted so the message is stable across runs; Go map order is not.
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s: %s", name, strings.Join(e.Fields[name], "; ")))
	}
	return "services: validation failed: " + strings.Join(parts, ", ")
}

// Invalid builds a single-field ValidationError, which is what a Validate
// method reaches for most often.
func Invalid(field string, messages ...string) *ValidationError {
	return &ValidationError{Fields: map[string][]string{field: messages}}
}
