package services

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// upper is a formatter with no domain in it, so these tests are about the walk
// rather than about money or time. The domain formatters live with the domain,
// which is the consumer's, and are exercised in example/.
func upper(_ context.Context, _ any, v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, errString("not a string")
	}
	return strings.ToUpper(s), nil
}

func marked(name string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Extra: map[string]any{RenderKeyword: name}}
}

func rendered(t *testing.T, r *Renderer, schema *jsonschema.Schema, raw string) string {
	t.Helper()
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("Unmarshal(%s): %v", raw, err)
	}
	out, err := r.Render(t.Context(), nil, schema, decoded)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return string(encoded)
}

func upperRenderer() *Renderer {
	return NewRenderer(map[string]Formatter{"upper": upper})
}

func TestRenderAppliesAFormatterWhereTheSchemaAsksForOne(t *testing.T) {
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"shout": marked("upper"),
			"quiet": {Type: "string"},
		},
	}

	got := rendered(t, upperRenderer(), schema, `{"shout":"hi","quiet":"hi"}`)

	if got != `{"quiet":"hi","shout":"HI"}` {
		t.Errorf("rendered = %s, want only the marked field changed", got)
	}
}

// Nesting is where a walk goes wrong, so both containers are driven rather than
// assumed from the flat case.
func TestRenderDescendsObjectsAndArrays(t *testing.T) {
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"rows": {
				Type: "array",
				Items: &jsonschema.Schema{
					Type:       "object",
					Properties: map[string]*jsonschema.Schema{"name": marked("upper")},
				},
			},
		},
	}

	got := rendered(t, upperRenderer(), schema, `{"rows":[{"name":"a"},{"name":"b"}]}`)

	if got != `{"rows":[{"name":"A"},{"name":"B"}]}` {
		t.Errorf("rendered = %s, want every element rendered", got)
	}
}

// A schema the walk has nothing to say about must not eat the value.
func TestRenderLeavesUnmarkedAndUnknownAlone(t *testing.T) {
	for name, schema := range map[string]*jsonschema.Schema{
		"no schema at all":  nil,
		"no keyword":        {Type: "string"},
		"unknown keyword":   marked("nobody-registered-this"),
		"keyword not a str": {Type: "string", Extra: map[string]any{RenderKeyword: 7}},
	} {
		if got := rendered(t, upperRenderer(), schema, `"hi"`); got != `"hi"` {
			t.Errorf("%s: rendered = %s, want the value untouched", name, got)
		}
	}
}

// A scalar under an object schema, and an object under a scalar schema: the walk
// follows the value's shape, because the value is what is being rendered.
func TestRenderFollowsTheValueWhenTheSchemaDisagrees(t *testing.T) {
	object := &jsonschema.Schema{
		Type:       "object",
		Properties: map[string]*jsonschema.Schema{"name": marked("upper")},
	}

	if got := rendered(t, upperRenderer(), object, `"scalar"`); got != `"scalar"` {
		t.Errorf("scalar under an object schema = %s, want it untouched", got)
	}
	if got := rendered(t, upperRenderer(), &jsonschema.Schema{Type: "string"}, `{"a":1}`); got != `{"a":1}` {
		t.Errorf("object under a scalar schema = %s, want it untouched", got)
	}
}

// A failure has to name where it happened. "could not render" on its own leaves
// a reader to find which field of which row refused.
func TestRenderNamesTheFieldThatFailed(t *testing.T) {
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"rows": {
				Type: "array",
				Items: &jsonschema.Schema{
					Type:       "object",
					Properties: map[string]*jsonschema.Schema{"name": marked("upper")},
				},
			},
		},
	}
	var decoded any
	if err := json.Unmarshal([]byte(`{"rows":[{"name":"ok"},{"name":7}]}`), &decoded); err != nil {
		t.Fatal(err)
	}

	_, err := upperRenderer().Render(t.Context(), nil, schema, decoded)

	if err == nil {
		t.Fatal("a failing formatter was not reported")
	}
	for _, want := range []string{"rows", "[1]", "name", "upper"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not locate %s", err, want)
		}
	}
}

// The renderer owns its formatters. A caller that keeps the map it passed in and
// mutates it later must not be able to change what a built renderer does.
func TestARendererDoesNotShareTheCallersMap(t *testing.T) {
	formatters := map[string]Formatter{"upper": upper}
	r := NewRenderer(formatters)
	delete(formatters, "upper")

	if got := rendered(t, r, marked("upper"), `"hi"`); got != `"HI"` {
		t.Errorf("rendered = %s, want the renderer to have kept its own formatter", got)
	}
}

// RenderValue is what an adapter calls: it does the JSON round trip Render
// needs its input already to have had, so a Go value goes in and the rendered
// decoded form comes out.
func TestRenderValueRoundTripsAGoValue(t *testing.T) {
	type row struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"name":  marked("upper"),
			"count": {Type: "integer"},
		},
	}

	out, err := upperRenderer().RenderValue(t.Context(), nil, schema, row{Name: "ada", Count: 2})
	if err != nil {
		t.Fatalf("RenderValue: %v", err)
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(encoded) != `{"count":2,"name":"ADA"}` {
		t.Errorf("rendered = %s, want the marked field rendered and the rest intact", encoded)
	}
}

// A value that cannot be marshalled is a bug in the service that produced it,
// and it has to surface as an error rather than as a half-rendered payload.
func TestRenderValueReportsAnUnencodableValue(t *testing.T) {
	_, err := upperRenderer().RenderValue(t.Context(), nil, marked("upper"), make(chan int))

	if err == nil {
		t.Fatal("an unencodable value was not reported")
	}
}

// A Go map reflects to a schema with no properties at all: its value schema
// lands under additionalProperties, which is the object-shaped counterpart of
// an array's items. Walking properties alone therefore drops the keyword on
// every entry, and drops it SILENTLY -- the schema still says the field is
// money, the transport still forwards it, and the reader gets the integer.
//
// Found by a consumer outside this repository, on an MCP wire where one payload
// carried the same amount twice: rendered in an array of line items, raw in the
// map of totals beside it.
func TestRenderDescendsIntoAMapsValueSchema(t *testing.T) {
	schema := &jsonschema.Schema{
		Type:                 "object",
		AdditionalProperties: marked("upper"),
	}

	got := rendered(t, upperRenderer(), schema, `{"first":"ada","second":"grace"}`)

	if got != `{"first":"ADA","second":"GRACE"}` {
		t.Errorf("rendered = %s, want every value rendered", got)
	}
}

// The two halves of a schema that has both. A struct with named fields AND an
// open tail is legal, and a walk that consulted only one of them would render
// half the object.
func TestRenderPrefersANamedPropertyOverTheOpenTail(t *testing.T) {
	schema := &jsonschema.Schema{
		Type:                 "object",
		Properties:           map[string]*jsonschema.Schema{"quiet": {Type: "string"}},
		AdditionalProperties: marked("upper"),
	}

	got := rendered(t, upperRenderer(), schema, `{"quiet":"hi","extra":"hi"}`)

	if got != `{"extra":"HI","quiet":"hi"}` {
		t.Errorf("rendered = %s, want the declared field left alone and the tail rendered", got)
	}
}

// additionalProperties: false is how a closed struct is spelt, and the
// jsonschema package carries it as a schema whose every keyword is unset. A
// fallback that did not distinguish "no tail schema" from "a tail schema with
// nothing in it" would still be correct here, which is why this asserts the
// value survives rather than asserting an internal.
func TestRenderLeavesAClosedObjectsUnknownFieldsAlone(t *testing.T) {
	schema := &jsonschema.Schema{
		Type:                 "object",
		Properties:           map[string]*jsonschema.Schema{"shout": marked("upper")},
		AdditionalProperties: &jsonschema.Schema{},
	}

	got := rendered(t, upperRenderer(), schema, `{"shout":"hi","stray":"hi"}`)

	if got != `{"shout":"HI","stray":"hi"}` {
		t.Errorf("rendered = %s, want the stray value untouched", got)
	}
}
