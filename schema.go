package services

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// SchemaFor lets a type declare the JSON Schema it marshals to, overriding what
// reflection would otherwise produce for its Go representation.
//
// Reach for it whenever a type's JSON form differs from its struct form -- the
// case reflection cannot see, because jsonschema-go consults json.Marshaler
// only for a fixed set of standard-library types. Optional implements it for
// exactly that reason.
//
// JSONSchema must not read its receiver: the kernel calls it on a zero value of
// the type while building the schema, long before any request exists.
type SchemaFor interface {
	JSONSchema() (*jsonschema.Schema, error)
}

var schemaForType = reflect.TypeFor[SchemaFor]()

// reflectSchema derives the JSON Schema for t, honouring any SchemaFor
// declarations found anywhere in its type graph.
//
// Required-ness comes from the struct tags rather than from anything here:
// jsonschema-go treats a field marked omitempty or omitzero as optional and
// every other exported field as required, which is the semantics we want and
// costs nothing to obtain.
func reflectSchema(t reflect.Type) (*jsonschema.Schema, error) {
	overrides := map[reflect.Type]*jsonschema.Schema{}
	if err := collectSchemaOverrides(t, overrides, map[reflect.Type]bool{}); err != nil {
		return nil, err
	}
	opts := &jsonschema.ForOptions{}
	if len(overrides) > 0 {
		opts.TypeSchemas = overrides
	}
	s, err := jsonschema.ForType(t, opts)
	if err != nil {
		return nil, fmt.Errorf("deriving schema for %s: %w", t, err)
	}
	return s, nil
}

// collectSchemaOverrides walks t's type graph and records the schema every
// SchemaFor-implementing type declares for itself.
func collectSchemaOverrides(
	t reflect.Type,
	out map[reflect.Type]*jsonschema.Schema,
	seen map[reflect.Type]bool,
) error {
	if t == nil || seen[t] {
		return nil
	}
	seen[t] = true

	// A pointer inherits its element's method set, so *T satisfies SchemaFor
	// whenever T does -- and reflect.Zero of a pointer type is nil, so calling
	// the method on it panics. Descend instead: the element carries the
	// override, and the reflector keeps the pointer's nullability.
	if t.Implements(schemaForType) && t.Kind() != reflect.Pointer {
		declared, err := reflect.Zero(t).Interface().(SchemaFor).JSONSchema()
		if err != nil {
			return fmt.Errorf("%s declared an invalid schema: %w", t, err)
		}
		if declared == nil {
			return fmt.Errorf("%s returned a nil schema from JSONSchema", t)
		}
		if err := checkDeclarationMarshals(t, declared); err != nil {
			return err
		}
		out[t] = declared
		// A type that speaks for itself is not descended into: its internals
		// are exactly what the declaration exists to hide.
		return nil
	}

	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		return collectSchemaOverrides(t.Elem(), out, seen)
	case reflect.Struct:
		for i := range t.NumField() {
			if f := t.Field(i); f.IsExported() {
				if err := collectSchemaOverrides(f.Type, out, seen); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkDeclarationMarshals refuses a JSONSchema that its own type cannot serve.
//
// A SchemaFor declaration is the one place the advertised schema stops being
// derived from the Go type and starts being asserted about it. Nothing else
// checks the assertion: the kernel validates a request against the *input*
// schema on every dispatch and never validates a response at all, so a type
// promising a string and marshalling to an object advertises one thing, serves
// another, and reports nothing anywhere -- not through the kernel, not through
// an adapter, not through the MCP SDK's own result handling.
//
// The trap that produced this is worth naming, because it is the obvious
// spelling. `type DueDate time.Time` is a *defined* type, so it does not inherit
// time.Time's MarshalJSON; time.Time's fields are unexported, so it marshals to
// `{}`. Declaring `{"type":"string","format":"date-time"}` beside it is then a
// lie the compiler is happy with. The embedded spelling, `struct{ time.Time }`,
// keeps the method and is correct.
//
// Only the JSON *kind* is compared, deliberately. Validating a zero value
// against the whole declaration would refuse honest types: an enum's zero value
// is rarely one of its own members -- `LoanStatus("")` is not `on_loan` -- and
// the same goes for a minLength, a pattern or any other constraint a real value
// satisfies and an empty one does not. The kind is the part a zero value can
// speak for, and a kind mismatch is the whole of the defect this catches.
func checkDeclarationMarshals(t reflect.Type, declared *jsonschema.Schema) error {
	types := declared.Types
	if declared.Type != "" {
		types = append([]string{declared.Type}, types...)
	}
	if len(types) == 0 {
		// A declaration that names no type promises nothing about the kind, so
		// there is nothing here to contradict.
		return nil
	}

	encoded, err := json.Marshal(reflect.Zero(t).Interface())
	if err != nil {
		return fmt.Errorf("%s declares a schema but its zero value cannot be marshalled: %w", t, err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return fmt.Errorf("%s marshals to something that is not JSON: %w", t, err)
	}

	got := jsonKind(decoded)
	if slices.Contains(types, got) {
		return nil
	}
	// "integer" is a JSON number that happens to be whole. A zero value is
	// whole by construction, so a type declaring "number" is served correctly
	// by one and must not be refused for it.
	if got == "integer" && slices.Contains(types, "number") {
		return nil
	}
	return fmt.Errorf(
		"services: %s declares %q in JSONSchema but marshals to %s (zero value: %s); "+
			"a defined type over one with a MarshalJSON method does not inherit it -- "+
			"embed the type instead of defining over it, or declare the schema it "+
			"actually serves",
		t, strings.Join(types, "/"), got, encoded)
}

// jsonKind names the JSON Schema type of a decoded JSON value.
func jsonKind(v any) string {
	switch n := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64:
		if n == math.Trunc(n) {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	default:
		return "object"
	}
}
