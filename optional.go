package services

import (
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
)

// Optional distinguishes "the client did not send this field" from "the client
// sent this field's zero value".
//
// Go's zero value erases that distinction, which is fatal for PATCH: a
// Name string arriving as "" cannot be told from a Name that was never sent, so
// a naive update blanks every field the caller did not mention. This is the
// same problem djangorestframework-services spends its UNSET sentinel on.
//
// Declare it with the omitzero tag option, which encoding/json honours through
// IsZero (Go 1.24+):
//
//	type UpdateAuthorIn struct {
//	    Name Optional[string]  `json:"name,omitzero"`
//	    Bio  Optional[*string] `json:"bio,omitzero"`
//	}
//
// Nullability composes through the type parameter rather than needing a second
// flag: Optional[string] may be absent, and Optional[*string] may be absent or
// explicitly null. The zero Optional is unset.
type Optional[T any] struct {
	set   bool
	value T
}

// Some returns an Optional that is set to v.
func Some[T any](v T) Optional[T] { return Optional[T]{set: true, value: v} }

// Get returns the value and whether it was set.
func (o Optional[T]) Get() (T, bool) { return o.value, o.set }

// IsSet reports whether the field was present in the input, including when it
// was present and null.
func (o Optional[T]) IsSet() bool { return o.set }

// Or returns the value if set, and fallback otherwise.
func (o Optional[T]) Or(fallback T) T {
	if o.set {
		return o.value
	}
	return fallback
}

// IsZero makes encoding/json's omitzero option skip an unset Optional. Without
// it the field would always marshal, because omitempty does not skip structs.
func (o Optional[T]) IsZero() bool { return !o.set }

// UnmarshalJSON records that the field was present, then decodes it. A literal
// null still counts as present -- that is the whole point of the type.
func (o *Optional[T]) UnmarshalJSON(b []byte) error {
	o.set = true
	return json.Unmarshal(b, &o.value)
}

// MarshalJSON writes the value, so an Optional is invisible on the way out.
func (o Optional[T]) MarshalJSON() ([]byte, error) { return json.Marshal(o.value) }

// JSONSchema implements SchemaFor.
//
// It is required, not a nicety: jsonschema reflection does not consult
// json.Marshaler for arbitrary types, so without this an Optional[string] would
// advertise its own internals as an object with "set" and "value" properties.
// Verified against jsonschema-go v0.4.3 on 2026-09-03.
func (o Optional[T]) JSONSchema() (*jsonschema.Schema, error) {
	return jsonschema.For[T](nil)
}
