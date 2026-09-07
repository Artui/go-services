package services

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

// The unexported field is the point: a type declaring its own schema must
// advertise that schema and not its Go representation.
//
// MarshalJSON is not decoration. Without it this double claimed to be the very
// thing it stands in for -- a type whose JSON form differs from its struct form
// -- while having no marshaller at all, so its JSON form *was* its struct form
// and the declaration was a lie no test could see. The check in
// checkDeclarationMarshals found it on its first run.
type declaredString struct {
	internal int //nolint:unused // must never reach the advertised schema
}

func (declaredString) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{Type: "string"}, nil
}

func (declaredString) MarshalJSON() ([]byte, error) { return []byte(`"declared"`), nil }

type brokenSchema struct{}

func (brokenSchema) JSONSchema() (*jsonschema.Schema, error) {
	return nil, errString("no schema for you")
}

type nilSchema struct{}

func (nilSchema) JSONSchema() (*jsonschema.Schema, error) { return nil, nil }

type errString string

func (e errString) Error() string { return string(e) }

func TestReflectSchemaRequiredComesFromTags(t *testing.T) {
	type in struct {
		Must    string `json:"must"`
		Maybe   string `json:"maybe,omitempty"`
		Skipped string `json:"skipped,omitzero"`
		hidden  string //nolint:unused // unexported fields are not properties
	}
	s, err := reflectSchema(reflect.TypeFor[in]())
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Required; len(got) != 1 || got[0] != "must" {
		t.Errorf("Required = %v, want [must]", got)
	}
	if _, ok := s.Properties["hidden"]; ok {
		t.Error("unexported fields must not become properties")
	}
}

// The reason SchemaFor exists: reflection does not consult json.Marshaler, so
// without an override a wrapper advertises its own internals.
func TestReflectSchemaHonoursDeclaredSchemas(t *testing.T) {
	type in struct {
		Direct declaredString   `json:"direct"`
		Nested []declaredString `json:"nested"`
		Deep   struct {
			Inner declaredString `json:"inner"`
		} `json:"deep"`
		Ptr *declaredString           `json:"ptr,omitempty"`
		Map map[string]declaredString `json:"map,omitempty"`
	}
	s, err := reflectSchema(reflect.TypeFor[in]())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "internal") {
		t.Errorf("the declared schema did not replace the struct form: %s", b)
	}
	if got := s.Properties["direct"].Type; got != "string" {
		t.Errorf("direct type = %q, want string", got)
	}
	if got := s.Properties["nested"].Items.Type; got != "string" {
		t.Errorf("slice item type = %q, want string", got)
	}
	if got := s.Properties["deep"].Properties["inner"].Type; got != "string" {
		t.Errorf("nested struct field type = %q, want string", got)
	}
}

func TestReflectSchemaErrors(t *testing.T) {
	t.Run("a declaration that fails", func(t *testing.T) {
		type in struct {
			Bad brokenSchema `json:"bad"`
		}
		_, err := reflectSchema(reflect.TypeFor[in]())
		if err == nil || !strings.Contains(err.Error(), "no schema for you") {
			t.Errorf("want the declaration's own error, got %v", err)
		}
	})

	t.Run("a declaration returning nil", func(t *testing.T) {
		type in struct {
			Bad nilSchema `json:"bad"`
		}
		_, err := reflectSchema(reflect.TypeFor[in]())
		if err == nil || !strings.Contains(err.Error(), "nil schema") {
			t.Errorf("want a nil-schema error, got %v", err)
		}
	})

	t.Run("a type reflection cannot express", func(t *testing.T) {
		type in struct {
			Ch chan int `json:"ch"`
		}
		if _, err := reflectSchema(reflect.TypeFor[in]()); err == nil {
			t.Error("a channel field must be refused")
		}
	})

	t.Run("a cycle terminates", func(t *testing.T) {
		// The walk's seen set is what stops this recursing forever; the error
		// itself comes from the reflector.
		type node struct {
			Next *node `json:"next,omitempty"`
		}
		if _, err := reflectSchema(reflect.TypeFor[node]()); err == nil {
			t.Error("a cyclic type must be refused")
		}
	})
}

func TestReflectSchemaNilType(t *testing.T) {
	// Reached through the walk rather than the top level, but covered directly
	// so the guard is not load-bearing on an accident.
	if err := collectSchemaOverrides(nil, map[reflect.Type]*jsonschema.Schema{}, map[reflect.Type]bool{}); err != nil {
		t.Errorf("a nil type must be a no-op, got %v", err)
	}
}

// Regression: *T satisfies SchemaFor whenever T does, and reflect.Zero of a
// pointer type is nil, so treating the pointer as the declarer called a value
// method on a nil receiver and panicked.
func TestReflectSchemaPointerToDeclaringType(t *testing.T) {
	type in struct {
		Ptr *declaredString `json:"ptr,omitempty"`
	}
	s, err := reflectSchema(reflect.TypeFor[in]())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s.Properties["ptr"])
	if strings.Contains(string(b), "internal") {
		t.Errorf("the pointer's element must still carry its declared schema: %s", b)
	}
	t.Logf("pointer property schema: %s", b)
}

// A defined type over one with a MarshalJSON method does not inherit it, which
// is the trap this refuses. `type DueDate time.Time` compiles, registers, and
// then advertises a string while serving `{}` -- because time.Time's fields are
// unexported and its MarshalJSON did not come along.
type lyingDate time.Time

func (lyingDate) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{Type: "string", Format: "date-time"}, nil
}

func TestADeclarationItsOwnTypeCannotServeIsRefused(t *testing.T) {
	type out struct {
		DueAt lyingDate `json:"due_at"`
	}

	_, err := reflectSchema(reflect.TypeFor[out]())

	if err == nil {
		t.Fatal("accepted a type declaring a string that marshals to an object")
	}
	for _, want := range []string{"lyingDate", `"string"`, "object", "embed the type"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

// The embedded spelling keeps the method, and must still be accepted.
type honestDate struct{ time.Time }

func (honestDate) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{Type: "string", Format: "date-time"}, nil
}

func TestTheEmbeddedSpellingIsAccepted(t *testing.T) {
	type out struct {
		DueAt honestDate `json:"due_at"`
	}

	if _, err := reflectSchema(reflect.TypeFor[out]()); err != nil {
		t.Fatalf("refused a type that marshals to what it declares: %v", err)
	}
}

// The false positive this check is deliberately narrow to avoid. An enum's zero
// value is rarely one of its own members, so validating a zero value against the
// whole declaration would refuse an honest type. Only the kind is compared, and
// "" is a string.
type zeroOutsideItsEnum string

func (zeroOutsideItsEnum) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{Type: "string", Enum: []any{"on_loan", "overdue"}}, nil
}

func TestAnEnumWhoseZeroValueIsNotAMemberIsAccepted(t *testing.T) {
	type out struct {
		Status zeroOutsideItsEnum `json:"status"`
	}

	if _, err := reflectSchema(reflect.TypeFor[out]()); err != nil {
		t.Fatalf("refused an enum for having a zero value outside itself: %v", err)
	}
}

// A declaration naming no type promises nothing about the kind, so there is
// nothing for a zero value to contradict.
type declaresNoType struct{ internal int } //nolint:unused // never reaches the schema

func (declaresNoType) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{Description: "anything at all"}, nil
}

func TestADeclarationNamingNoTypeIsAccepted(t *testing.T) {
	type out struct {
		Thing declaresNoType `json:"thing"`
	}

	if _, err := reflectSchema(reflect.TypeFor[out]()); err != nil {
		t.Fatalf("refused a declaration that names no type: %v", err)
	}
}

// A zero integer is a whole JSON number, so a type declaring "number" is served
// correctly by one and must not be refused for it.
type declaresNumber int

func (declaresNumber) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{Type: "number"}, nil
}

func TestAWholeZeroSatisfiesANumberDeclaration(t *testing.T) {
	type out struct {
		Rate declaresNumber `json:"rate"`
	}

	if _, err := reflectSchema(reflect.TypeFor[out]()); err != nil {
		t.Fatalf("refused a number declaration served by a whole zero: %v", err)
	}
}

// Every kind a declaration can name, against a type that actually serves it.
// jsonKind decides what a mismatch message says, so a wrong branch here would
// misname the thing a reader is being told to fix.
type kindProbe struct {
	declared string
	marshals string
}

func (k kindProbe) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{Type: k.declared}, nil
}

func (k kindProbe) MarshalJSON() ([]byte, error) { return []byte(k.marshals), nil }

func TestEveryDeclarableKindIsRecognised(t *testing.T) {
	for _, c := range []struct{ declared, marshals string }{
		{"null", "null"},
		{"boolean", "true"},
		{"string", `"s"`},
		{"integer", "7"},
		{"number", "7.5"},
		{"number", "7"}, // a whole number still serves "number"
		{"array", "[1]"},
		{"object", "{}"},
	} {
		// The zero value is what gets marshalled, so the case has to be carried
		// by the type rather than by a value -- which is the same constraint a
		// real declaring type is under.
		if got := jsonKind(decodeForTest(t, c.marshals)); got != c.declared &&
			!(got == "integer" && c.declared == "number") {
			t.Errorf("jsonKind(%s) = %q, want %q", c.marshals, got, c.declared)
		}
	}
}

func decodeForTest(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("Unmarshal(%s): %v", raw, err)
	}
	return v
}

// A declaration listing several kinds is satisfied by any one of them, which is
// how a nullable field is spelt.
type declaresNullableString struct{}

func (declaresNullableString) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{Types: []string{"null", "string"}}, nil
}

func (declaresNullableString) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

func TestADeclarationNamingSeveralKindsAcceptsAnyOfThem(t *testing.T) {
	type out struct {
		Maybe declaresNullableString `json:"maybe"`
	}

	if _, err := reflectSchema(reflect.TypeFor[out]()); err != nil {
		t.Fatalf("refused a nullable declaration served by null: %v", err)
	}
}

// A MarshalJSON that fails is a type that cannot be served at all, and saying so
// at registration beats discovering it on the first response.
type marshalFails struct{}

func (marshalFails) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{Type: "string"}, nil
}

func (marshalFails) MarshalJSON() ([]byte, error) { return nil, errString("cannot marshal") }

func TestADeclaringTypeThatCannotMarshalIsRefused(t *testing.T) {
	type out struct {
		Bad marshalFails `json:"bad"`
	}

	_, err := reflectSchema(reflect.TypeFor[out]())

	if err == nil || !strings.Contains(err.Error(), "cannot be marshalled") {
		t.Fatalf("err = %v, want it to name the failed marshal", err)
	}
}

// A MarshalJSON returning bytes that are not JSON is refused, and by the earlier
// arm rather than the later one: encoding/json compacts what MarshalJSON returns
// and fails there, so nothing json.Marshal accepts can fail to decode. That is
// why the Unmarshal arm beside it is unreachable and named in the exclusions.
type marshalsNonJSON struct{}

func (marshalsNonJSON) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{Type: "string"}, nil
}

func (marshalsNonJSON) MarshalJSON() ([]byte, error) { return []byte(`not json`), nil }

func TestADeclaringTypeThatMarshalsNonJSONIsRefused(t *testing.T) {
	type out struct {
		Bad marshalsNonJSON `json:"bad"`
	}

	_, err := reflectSchema(reflect.TypeFor[out]())

	if err == nil {
		t.Fatal("accepted a type whose MarshalJSON returns something that is not JSON")
	}
	if !strings.Contains(err.Error(), "cannot be marshalled") {
		t.Errorf("err = %v, want the marshal arm -- if this moves, the exclusion "+
			"for the unmarshal arm has stopped being true", err)
	}
}
