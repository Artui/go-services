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
