package mcpx

// The two checks that cannot be reached from outside the package: one because
// it is about object identity rather than about bytes, and one because the
// kernel never produces the input it guards against.

import (
	"strings"
	"testing"

	"github.com/Artui/go-services"
	"github.com/google/jsonschema-go/jsonschema"
)

// TestTheAdvertisedSchemaIsTheKernelsOwnObject is the structural half of the
// library's central claim, and the reason a client can be shown the enforced
// contract rather than a rendering of it.
//
// Copying the schema is the easy mistake: a copy is correct on the day it is
// made and silently stale the moment anything touches the original -- a Schema
// hook, a later kernel enrichment, a resolver that annotates in place. Pointer
// equality is the only assertion that rules the copy out, and it is not
// expressible through a wire test, because two equal schemas serialise
// identically.
func TestTheAdvertisedSchemaIsTheKernelsOwnObject(t *testing.T) {
	reg := services.New[struct{}](nil)
	type in struct {
		ID int `json:"id"`
	}
	err := services.Register(reg, services.Spec[struct{}, in, in]{
		Name: "thing",
		Kind: services.Query,
		Run:  func(_ services.Ctx[struct{}], v in) (in, error) { return v, nil },
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	entry := reg.Entries()[0]
	tool, err := toolFor(entry)
	if err != nil {
		t.Fatalf("toolFor: %v", err)
	}
	if tool.InputSchema != any(entry.Input) {
		t.Error("the advertised input schema is a different object from the enforced one")
	}
	if tool.OutputSchema != any(entry.Output) {
		t.Error("the advertised output schema is a different object from the entry's")
	}
}

// TestToolForGuardsAgainstAnEntryWithNoSchema covers a state the Registry
// cannot produce: Register always reflects an input schema, so Entries never
// yields one without.
//
// The guard stays because the alternative is a panic from inside the SDK. Entry
// is an exported struct with exported fields, so an Entry with no schema is
// constructible by anything that builds one by hand, and the failure it would
// otherwise cause is the least debuggable kind.
func TestToolForGuardsAgainstAnEntryWithNoSchema(t *testing.T) {
	_, err := toolFor(services.Entry{Name: "thing", Kind: services.Query})
	if err == nil {
		t.Fatal("toolFor accepted an entry with no input schema")
	}
	if !strings.Contains(err.Error(), "no schema at all") {
		t.Errorf("toolFor: %v", err)
	}
}

// TestCheckNameRejectsAnEmptyName covers the other half of the same situation.
// Register refuses an unnamed spec, so this is unreachable through a Registry,
// and the SDK would only log it.
func TestCheckNameRejectsAnEmptyName(t *testing.T) {
	if err := checkName(""); err == nil {
		t.Fatal("checkName accepted an empty name")
	}
}

// TestSchemaTypeNameReportsWhatWasDeclared pins the wording of the message a
// spec author reads when their input cannot be a tool. Each case is a shape
// jsonschema-go actually produces.
func TestSchemaTypeNameReportsWhatWasDeclared(t *testing.T) {
	for _, tc := range []struct {
		schema *jsonschema.Schema
		want   string
	}{
		{nil, "no schema at all"},
		{&jsonschema.Schema{Type: "string"}, `"string"`},
		{&jsonschema.Schema{Types: []string{"null", "array"}}, `"null or array"`},
		{&jsonschema.Schema{}, "no type at all"},
	} {
		if got := schemaTypeName(tc.schema); got != tc.want {
			t.Errorf("schemaTypeName(%v) = %s, want %s", tc.schema, got, tc.want)
		}
	}
}

// TestAValidationErrorWithNothingToSayStillReads covers the two shapes that
// leave the rendering with no line to print: no fields at all, and a field
// blamed with no message. Invalid takes its messages variadically, so the
// second is one forgotten argument away and produces a heading followed by
// nothing.
func TestAValidationErrorWithNothingToSayStillReads(t *testing.T) {
	const want = "The arguments were rejected, with no reason given."
	for _, e := range []*services.ValidationError{
		{},
		services.Invalid("email"),
	} {
		if got := explainValidation(e); got != want {
			t.Errorf("explainValidation(%v) = %q, want %q", e.Fields, got, want)
		}
	}
}

// TestTheDestructiveHintIsCopiedNotShared pins the deliberate contrast with
// the schema pointers above.
//
// A schema is shared on purpose, because a copy could drift from the object the
// kernel enforces. A hint has nothing to drift from, so passing the caller's
// pointer through to a published annotation would only create a way for a later
// write to that bool to silently rewrite what every listing client is told.
//
// Register detaches the flag as well, which means this asserts a property two
// layers now guarantee. That is deliberate: this test states the kernel's
// contract from outside the kernel, so it fails if that contract regresses
// whether or not the kernel's own test does. Do not delete either one on the
// grounds that the other exists.
func TestTheDestructiveHintIsCopiedNotShared(t *testing.T) {
	declared := false
	entry := services.Entry{
		Name:        "thing",
		Kind:        services.Mutation,
		Destructive: &declared,
		Input:       &jsonschema.Schema{Type: "object"},
	}

	tool, err := toolFor(entry)
	if err != nil {
		t.Fatalf("toolFor: %v", err)
	}
	hint := tool.Annotations.DestructiveHint
	if hint == nil || *hint != false {
		t.Fatalf("destructive hint %v, want a declared false", hint)
	}
	if hint == &declared {
		t.Fatal("the annotation shares the spec's pointer, so writing through it rewrites what clients were told")
	}

	declared = true
	if *tool.Annotations.DestructiveHint {
		t.Error("writing to the spec's bool changed an already-published annotation")
	}
}
