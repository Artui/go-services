package services

import (
	"fmt"
	"reflect"

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
