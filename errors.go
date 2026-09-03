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
)

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
