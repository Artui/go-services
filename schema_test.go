package services

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

type declaredString struct{ internal int }

func (declaredString) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{Type: "string"}, nil
}

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
