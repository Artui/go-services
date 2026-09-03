package services

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

type paramsIn struct {
	Name   string   `json:"name,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	Ratio  float64  `json:"ratio,omitempty"`
	Active bool     `json:"active,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	Sizes  []int    `json:"sizes,omitempty"`
	Nick   *string  `json:"nick,omitempty"`
	Blob   any      `json:"blob,omitempty"`
}

func paramsSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	s, err := reflectSchema(reflect.TypeFor[paramsIn]())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEncodeParamsCoercesBySchema(t *testing.T) {
	s := paramsSchema(t)
	raw, err := EncodeParams(s, nil, map[string][]string{
		"name":   {"ada"},
		"limit":  {"10"},
		"ratio":  {"1.5"},
		"active": {"true"},
		"tags":   {"a", "b"},
		"sizes":  {"1", "2"},
		"nick":   {"lovelace"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	// The whole point: a query string is all strings, and 10 must not arrive
	// as "10".
	if got["limit"] != float64(10) {
		t.Errorf("limit = %#v, want the number 10", got["limit"])
	}
	if got["ratio"] != 1.5 {
		t.Errorf("ratio = %#v, want the number 1.5", got["ratio"])
	}
	if got["active"] != true {
		t.Errorf("active = %#v, want the boolean true", got["active"])
	}
	if got["name"] != "ada" {
		t.Errorf("name = %#v, want the string ada", got["name"])
	}
	// A nullable field still coerces as its real type rather than falling back.
	if got["nick"] != "lovelace" {
		t.Errorf("nick = %#v", got["nick"])
	}
	if !reflect.DeepEqual(got["tags"], []any{"a", "b"}) {
		t.Errorf("tags = %#v, want both values", got["tags"])
	}
	if !reflect.DeepEqual(got["sizes"], []any{float64(1), float64(2)}) {
		t.Errorf("sizes = %#v, want coerced numbers", got["sizes"])
	}

	// And the result actually satisfies the schema it was coerced against.
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	if err := rs.Validate(probe); err != nil {
		t.Errorf("coerced payload must validate, got %v", err)
	}
}

func TestEncodeParamsCoercionFailures(t *testing.T) {
	s := paramsSchema(t)
	for _, tc := range []struct {
		field, value, want string
	}{
		{"limit", "ten", "an integer"},
		{"ratio", "half", "a number"},
		{"active", "yes please", "a boolean"},
		{"sizes", "one", "an integer"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			_, err := EncodeParams(s, nil, map[string][]string{tc.field: {tc.value}}, nil)
			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("got %v, want a ValidationError", err)
			}
			// This is the one place the kernel can attribute a message to a
			// field, because it knows which parameter it was coercing.
			msgs, ok := invalid.Fields[tc.field]
			if !ok {
				t.Fatalf("fields %v, want a %q key", invalid.Fields, tc.field)
			}
			if len(msgs) != 1 || !strings.Contains(msgs[0], tc.want) {
				t.Errorf("message %v, want one mentioning %q", msgs, tc.want)
			}
		})
	}
}

func TestEncodeParamsPassesBodyThroughWithNoParams(t *testing.T) {
	body := json.RawMessage(`{"name":"ada"}`)
	got, err := EncodeParams(paramsSchema(t), body, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Errorf("got %s, want the body untouched", got)
	}
}

func TestEncodeParamsOverlaysOntoBody(t *testing.T) {
	got, err := EncodeParams(paramsSchema(t),
		json.RawMessage(`{"name":"ada","limit":99}`),
		map[string][]string{"limit": {"5"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatal(err)
	}
	if out["name"] != "ada" {
		t.Error("body fields must survive")
	}
	// Params win, so a route capture cannot be overridden from the body.
	if out["limit"] != float64(5) {
		t.Errorf("limit = %#v, want the parameter to win", out["limit"])
	}
}

func TestEncodeParamsIgnoresWhatTheSchemaDoesNotDeclare(t *testing.T) {
	// A query string carries analytics noise that is none of the operation's
	// business; rejecting it would break real clients.
	got, err := EncodeParams(paramsSchema(t), nil, map[string][]string{
		"utm_source": {"newsletter"},
		"name":       {"ada"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out["utm_source"]; ok {
		t.Error("an undeclared parameter must be dropped, not carried")
	}
	if out["name"] != "ada" {
		t.Error("a declared parameter must still land")
	}
}

func TestEncodeParamsEdgeCases(t *testing.T) {
	s := paramsSchema(t)

	t.Run("an empty value slice is skipped", func(t *testing.T) {
		got, err := EncodeParams(s, nil, map[string][]string{"name": {}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `{}` {
			t.Errorf("got %s, want an empty object", got)
		}
	})

	t.Run("a repeated scalar takes the first", func(t *testing.T) {
		got, err := EncodeParams(s, nil, map[string][]string{"name": {"first", "second"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.Unmarshal(got, &out)
		if out["name"] != "first" {
			t.Errorf("name = %#v, want the first value, matching url.Values.Get", out["name"])
		}
	})

	t.Run("an untyped property passes through as a string", func(t *testing.T) {
		got, err := EncodeParams(s, nil, map[string][]string{"blob": {"anything"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.Unmarshal(got, &out)
		if out["blob"] != "anything" {
			t.Errorf("blob = %#v, want the raw string for the schema to judge", out["blob"])
		}
	})

	t.Run("a malformed body is a validation error", func(t *testing.T) {
		_, err := EncodeParams(s, json.RawMessage(`{`), map[string][]string{"name": {"a"}}, nil)
		var invalid *ValidationError
		if !errors.As(err, &invalid) {
			t.Fatalf("got %v, want a ValidationError", err)
		}
	})

	t.Run("a nil schema declares nothing", func(t *testing.T) {
		got, err := EncodeParams(nil, nil, map[string][]string{"name": {"a"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `{}` {
			t.Errorf("got %s, want an empty object", got)
		}
	})

	t.Run("schemaType reads a union past its null", func(t *testing.T) {
		if got := schemaType(nil); got != "" {
			t.Errorf("nil schema type = %q, want empty", got)
		}
		if got := schemaType(&jsonschema.Schema{Types: []string{"null"}}); got != "" {
			t.Errorf("null-only type = %q, want empty", got)
		}
	})
}

// The precedence is a security rule, not a convenience: an operation mounted at
// /tenants/{tenant}/reports is scoped by its path, and a merge in the other
// direction would let a client rescope it with ?tenant=other. The kernel does
// the merge so an adapter cannot get it backwards.
func TestEncodeParamsCaptureBeatsQueryBeatsBody(t *testing.T) {
	s := paramsSchema(t)
	raw, err := EncodeParams(s,
		json.RawMessage(`{"name":"from-body"}`),
		map[string][]string{"name": {"from-query"}},
		map[string][]string{"name": {"from-capture"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["name"] != "from-capture" {
		t.Errorf("name = %#v, want the route capture to win", got["name"])
	}

	// And query still beats body when no capture claims the key.
	raw, err = EncodeParams(s, json.RawMessage(`{"name":"from-body"}`),
		map[string][]string{"name": {"from-query"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &got)
	if got["name"] != "from-query" {
		t.Errorf("name = %#v, want the query to beat the body", got["name"])
	}
}

func TestEncodeParamsCoercionFailureInEitherSource(t *testing.T) {
	s := paramsSchema(t)
	for _, tc := range []struct {
		name            string
		query, captures map[string][]string
	}{
		{"query", map[string][]string{"limit": {"ten"}}, nil},
		{"captures", nil, map[string][]string{"limit": {"ten"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EncodeParams(s, nil, tc.query, tc.captures)
			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Errorf("got %v, want a ValidationError from the %s source", err, tc.name)
			}
		})
	}
}

// One condition, one wording. Which of the two kernel sites rejects a malformed
// body depends on whether the client appended a query parameter, which is not a
// distinction worth putting on the wire.
func TestMalformedBodyReadsTheSameFromBothSites(t *testing.T) {
	r := newTestRegistry(t)
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{Name: "greet", Kind: Query, Run: greet})

	_, viaDispatch := r.Dispatch(context.Background(), nil, "greet", []byte(`{`))
	_, viaParams := EncodeParams(paramsSchema(t), json.RawMessage(`{`),
		map[string][]string{"name": {"a"}}, nil)

	var a, b *ValidationError
	if !errors.As(viaDispatch, &a) || !errors.As(viaParams, &b) {
		t.Fatalf("both should be validation errors: %v / %v", viaDispatch, viaParams)
	}
	if a.Error() != b.Error() {
		t.Errorf("two wordings for one condition:\n  dispatch: %s\n  params:   %s", a, b)
	}
}

// A query parameter is client-supplied noise; a route capture was written into
// the pattern by hand and is always load-bearing. Dropping one silently is how
// /tenants/{tenant}/invoices runs unscoped against a spec with no tenant field.
func TestEncodeParamsRefusesAnUndeclaredCaptureButDropsAnUndeclaredQuery(t *testing.T) {
	s := paramsSchema(t)

	if _, err := EncodeParams(s, nil, nil, map[string][]string{"tenant": {"acme"}}); err == nil {
		t.Error("a capture the operation cannot receive must be refused, not discarded")
	} else {
		var invalid *ValidationError
		if !errors.As(err, &invalid) || len(invalid.Fields["tenant"]) == 0 {
			t.Errorf("got %v, want a ValidationError naming the capture", err)
		}
	}

	got, err := EncodeParams(s, nil, map[string][]string{"utm_source": {"newsletter"}}, nil)
	if err != nil {
		t.Fatalf("an undeclared query parameter must still be dropped, got %v", err)
	}
	if string(got) != `{}` {
		t.Errorf("got %s, want the noise dropped", got)
	}
}

// The same client mistake must get the same explanation whether or not the
// route happens to capture anything.
func TestEncodeParamsPassesAValidNonObjectBodyThroughUnchanged(t *testing.T) {
	s := paramsSchema(t)
	body := json.RawMessage(`[1,2,3]`)

	for _, tc := range []struct {
		name  string
		query map[string][]string
	}{
		{"no parameters", nil},
		{"with a parameter", map[string][]string{"limit": {"5"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EncodeParams(s, body, tc.query, nil)
			if err != nil {
				t.Fatalf("a valid non-object body is the schema's to reject, got %v", err)
			}
			if string(got) != string(body) {
				t.Errorf("got %s, want the body untouched", got)
			}
		})
	}

	// And the schema is what rejects it, with an accurate message.
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	var probe any
	_ = json.Unmarshal(body, &probe)
	if rs.Validate(probe) == nil {
		t.Error("the schema should refuse an array where an object is declared")
	}
}
