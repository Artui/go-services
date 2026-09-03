package mcpx_test

// What a client is told about a tool before it calls one.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestEverySpecIsAdvertisedWithItsDescription(t *testing.T) {
	reg := newRegistry(t)
	cs := connect(t, reg, nil)

	advertised := tools(t, cs)
	for _, e := range reg.Entries() {
		tool, ok := advertised[e.Name]
		if !ok {
			t.Fatalf("%q was registered but not advertised", e.Name)
		}
		if tool.Description != e.Description {
			t.Errorf("%s: description %q, want %q", e.Name, tool.Description, e.Description)
		}
	}
	if len(advertised) != len(reg.Entries()) {
		t.Errorf("advertised %d tools for %d entries", len(advertised), len(reg.Entries()))
	}
}

// TestAnnotationsSayOnlyWhatTheSpecDeclared checks the hints a client reads
// against every combination of Kind, Idempotent and Destructive the kernel can
// express.
//
// Two rows carry the rule. authors.create declares neither optional hint and so
// publishes no annotations object at all, because attaching one would assert an
// idempotentHint nobody chose. authors.draft declares only Destructive and so
// must publish one -- and the undeclared idempotentHint: false that comes with
// it is the protocol's own default for a hint nobody set, which is the price of
// the SDK modelling that field as a plain bool.
func TestAnnotationsSayOnlyWhatTheSpecDeclared(t *testing.T) {
	advertised := tools(t, connect(t, newRegistry(t), nil))

	for _, tc := range []struct {
		tool string
		want *mcp.ToolAnnotations
		why  string
	}{
		{
			tool: "authors.get",
			want: &mcp.ToolAnnotations{ReadOnlyHint: true},
			why:  "a query with both optional hints undeclared: read-only is stated, and the hints that ride along are defined as meaningless while it is",
		},
		{
			tool: "authors.count",
			want: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
			why:  "a query that declared idempotency anyway",
		},
		{
			tool: "authors.replace",
			want: &mcp.ToolAnnotations{IdempotentHint: true},
			why:  "a mutation declaring only idempotency",
		},
		{
			tool: "authors.draft",
			want: &mcp.ToolAnnotations{DestructiveHint: truth(false)},
			why:  "a create declaring only that it is additive, which is what stops an approval gate over-prompting",
		},
		{
			tool: "authors.delete",
			want: &mcp.ToolAnnotations{DestructiveHint: truth(true)},
			why:  "a mutation declaring both, though only the destructive half is legible on the wire",
		},
		{
			tool: "authors.create",
			want: nil,
			why:  "a mutation declaring neither publishes nothing rather than two values it never claimed",
		},
	} {
		got := advertised[tc.tool].Annotations
		// DeepEqual rather than a struct comparison: DestructiveHint is a
		// pointer, so == would compare addresses and pass for nothing.
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: annotations %s, want %s (%s)",
				tc.tool, showAnnotations(got), showAnnotations(tc.want), tc.why)
		}
	}
}

// showAnnotations renders annotations for a failure message. The default
// formatting prints DestructiveHint as an address, which is the one field these
// assertions turn on.
func showAnnotations(a *mcp.ToolAnnotations) string {
	if a == nil {
		return "none"
	}
	destructive := "unset"
	if a.DestructiveHint != nil {
		destructive = fmt.Sprintf("%t", *a.DestructiveHint)
	}
	return fmt.Sprintf("{readOnly:%t idempotent:%t destructive:%s}",
		a.ReadOnlyHint, a.IdempotentHint, destructive)
}

// TestAnnotationsOnTheWireAreExactlyTheseFrames is the same rule checked
// against the recorded bytes rather than the client's decoder.
//
// It is worth doing twice. The SDK decodes an absent annotations object and an
// empty one into Go values that are easy to confuse and are not the same JSON,
// and the whole point of omitting the object is a distinction that exists only
// on the wire. Pinning the frames also means the day the SDK gives
// IdempotentHint an omitempty, the test that fails is the one carrying the
// explanation of why the omission logic is shaped like this.
func TestAnnotationsOnTheWireAreExactlyTheseFrames(t *testing.T) {
	cs, tap := tapped(t, newRegistry(t))
	if _, err := cs.ListTools(t.Context(), nil); err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	var listing struct {
		Tools []struct {
			Name        string          `json:"name"`
			Annotations json.RawMessage `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(tap.lastResult(t), &listing); err != nil {
		t.Fatalf("decoding the recorded tools/list: %v", err)
	}

	want := map[string]string{
		"authors.get":     `{"idempotentHint":false,"readOnlyHint":true}`,
		"authors.count":   `{"idempotentHint":true,"readOnlyHint":true}`,
		"authors.replace": `{"idempotentHint":true,"readOnlyHint":false}`,
		"authors.draft":   `{"destructiveHint":false,"idempotentHint":false,"readOnlyHint":false}`,
		"authors.delete":  `{"destructiveHint":true,"idempotentHint":false,"readOnlyHint":false}`,

		// The empty string is the assertion: no annotations key at all.
		"authors.create": "",
	}
	checked := 0
	for _, tool := range listing.Tools {
		expected, interesting := want[tool.Name]
		if !interesting {
			continue
		}
		checked++
		if string(tool.Annotations) != expected {
			t.Errorf("%s: annotations on the wire %q, want %q", tool.Name, tool.Annotations, expected)
		}
	}
	if checked != len(want) {
		t.Errorf("checked %d tools, want %d -- a fixture was renamed", checked, len(want))
	}
}

// TestADeclaredFalseIdempotencyIsIndistinguishableOnTheWire records the one
// kernel state the protocol cannot carry, so that it is a known cost rather
// than a surprise.
//
// authors.delete declares Idempotent false and authors.create declares nothing,
// and their idempotentHint is identical. Only the destructiveHint beside it
// tells the two frames apart, which is exactly why Destructive being a *bool
// with omitempty in the SDK matters and IdempotentHint not being one does.
func TestADeclaredFalseIdempotencyIsIndistinguishableOnTheWire(t *testing.T) {
	advertised := tools(t, connect(t, newRegistry(t), nil))

	declaredFalse := advertised["authors.delete"].Annotations
	if declaredFalse.IdempotentHint {
		t.Fatal("authors.delete declares Idempotent false")
	}
	// The kernel can tell these apart. Nothing a client receives can.
	if advertised["authors.create"].Annotations != nil {
		t.Fatal("authors.create declares nothing and should carry no annotations")
	}
}

// TestANonObjectOutputSchemaIsPublishedAsDeclared covers the spec whose Out is
// a bare int.
//
// It is worth its own test because the obvious defensive move -- refusing an
// output schema that is not an object -- would be the adapter overruling a
// declaration it does not own. SEP-2106 allows structured content to be any
// JSON value, and the kernel let the author declare one, so the mount publishes
// what was declared.
func TestANonObjectOutputSchemaIsPublishedAsDeclared(t *testing.T) {
	cs := connect(t, newRegistry(t), nil)

	shown, err := json.Marshal(tools(t, cs)["authors.count"].OutputSchema)
	if err != nil {
		t.Fatalf("encoding the advertised output schema: %v", err)
	}
	if string(shown) != `{"type":"integer"}` {
		t.Errorf("output schema %s, want an integer schema", shown)
	}

	res := call(t, cs, "authors.count", map[string]any{})
	if res.IsError {
		t.Fatalf("call refused: %s", text(t, res))
	}
	if got := text(t, res); got != "2" {
		t.Errorf("text block %q, want %q", got, "2")
	}
}
