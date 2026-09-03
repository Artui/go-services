package services

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestOptionalZeroValueIsUnset(t *testing.T) {
	var o Optional[string]
	if o.IsSet() {
		t.Error("the zero Optional must be unset")
	}
	if !o.IsZero() {
		t.Error("IsZero must be true when unset, or omitzero will not skip it")
	}
	if got := o.Or("fallback"); got != "fallback" {
		t.Errorf("Or = %q, want the fallback", got)
	}
	if v, ok := o.Get(); ok || v != "" {
		t.Errorf("Get = (%q, %v), want the zero value and false", v, ok)
	}
}

func TestOptionalSome(t *testing.T) {
	o := Some(42)
	if !o.IsSet() || o.IsZero() {
		t.Error("Some must produce a set Optional")
	}
	if v, ok := o.Get(); !ok || v != 42 {
		t.Errorf("Get = (%d, %v), want (42, true)", v, ok)
	}
	if got := o.Or(7); got != 42 {
		t.Errorf("Or = %d, want the set value", got)
	}
}

// The distinction the type exists for: absent and explicitly null are different
// inputs, and only one of them means "change this field".
func TestOptionalAbsentVersusNull(t *testing.T) {
	type patch struct {
		Name Optional[string]  `json:"name,omitzero"`
		Bio  Optional[*string] `json:"bio,omitzero"`
	}

	var absent patch
	if err := json.Unmarshal([]byte(`{}`), &absent); err != nil {
		t.Fatal(err)
	}
	if absent.Name.IsSet() || absent.Bio.IsSet() {
		t.Error("an omitted field must stay unset")
	}

	var explicit patch
	if err := json.Unmarshal([]byte(`{"bio":null}`), &explicit); err != nil {
		t.Fatal(err)
	}
	if !explicit.Bio.IsSet() {
		t.Error("an explicit null must count as set")
	}
	if v, _ := explicit.Bio.Get(); v != nil {
		t.Errorf("an explicit null must decode to a nil value, got %v", v)
	}
}

func TestOptionalUnmarshalPropagatesError(t *testing.T) {
	var o Optional[int]
	if err := o.UnmarshalJSON([]byte(`"not a number"`)); err == nil {
		t.Error("a type mismatch inside the value must surface")
	}
}

func TestOptionalMarshalIsTransparent(t *testing.T) {
	type wrapper struct {
		Name Optional[string] `json:"name,omitzero"`
		Age  Optional[int]    `json:"age,omitzero"`
	}
	got, err := json.Marshal(wrapper{Name: Some("ada")})
	if err != nil {
		t.Fatal(err)
	}
	// The unset field is skipped by omitzero, and the set one shows its value
	// rather than the Optional's internals.
	if string(got) != `{"name":"ada"}` {
		t.Errorf("got %s, want {\"name\":\"ada\"}", got)
	}
}

// Optional's schema override has to survive the real reflection path, not only
// a direct call: without it an Optional[string] advertises "set" and "value".
func TestOptionalSchemaThroughReflection(t *testing.T) {
	type patch struct {
		Name Optional[string] `json:"name,omitzero"`
	}
	s, err := reflectSchema(reflect.TypeFor[patch]())
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Properties["name"].Type; got != "string" {
		t.Errorf("Optional[string] advertised %q, want string", got)
	}
	if len(s.Required) != 0 {
		t.Errorf("Required = %v, want omitzero to make it optional", s.Required)
	}
}
