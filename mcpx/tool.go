package mcpx

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Artui/go-services"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolFor turns one registered spec into the tool definition a client lists.
//
// Everything it reads is a declaration the spec author made once. Nothing here
// infers: an adapter that guessed a hint from a name or a signature would be a
// second place the truth lives, and the two would drift.
func toolFor(e services.Entry) (*mcp.Tool, error) {
	if err := checkName(e.Name); err != nil {
		return nil, err
	}
	// AddTool panics on a non-object input schema rather than returning an
	// error, and a spec whose In is a slice, a pointer or a bare scalar
	// reflects to exactly that. Catching it here turns a panic during wiring
	// into an error the caller can report, and the message repeats what the
	// schema said because the panic's would not name the spec.
	if e.Input == nil || e.Input.Type != "object" {
		return nil, fmt.Errorf(
			"mcpx: %q: an MCP tool's input schema must be an object, and this spec's input declares %s",
			e.Name, schemaTypeName(e.Input),
		)
	}

	return &mcp.Tool{
		Name:        e.Name,
		Description: e.Description,

		// The kernel's own schema pointers go on the wire, not copies of them.
		// This is the library's central claim made structural: no second object
		// exists that could disagree with the one Dispatch validates against,
		// because no second object exists.
		InputSchema:  e.Input,
		OutputSchema: e.Output,

		Annotations: annotationsFor(e),
	}, nil
}

// annotationsFor derives the tool hints from what the spec declared, and only
// from that.
//
// The kernel states three of MCP's four hints, and they do not reach the wire
// on equal terms:
//
//   - readOnlyHint comes from Kind, which is required, so it is always known.
//   - destructiveHint comes from Destructive, and ToolAnnotations models it as
//     a *bool with omitempty. Three states in, three states out: this one
//     survives the trip intact.
//   - idempotentHint comes from Idempotent, and ToolAnnotations models it as a
//     plain bool whose JSON tag carries no omitempty as of v1.7.0. Any
//     annotations block we attach therefore puts "idempotentHint": false on the
//     wire, declared or not.
//
// openWorldHint has no kernel field, so it is never set and takes its MCP
// default of true. That costs less than the destructive gap did: no approval
// policy is keyed on it.
//
// The rule that keeps the idempotency asymmetry honest is to attach the block
// only when it carries at least one fact somebody actually declared, and never
// to let a hint we cannot omit be the reason for attaching it:
//
//   - A mutation with nothing declared gets no block at all. Absent, both
//     hints take the MCP defaults of false, which is precisely "it has side
//     effects, and nothing was said about repeating it".
//   - A query needs the block, to carry readOnlyHint: true. The idempotentHint
//     riding along is not a claim, because the specification defines that hint
//     as meaningful only while readOnlyHint is false.
//   - A mutation declaring only one of the two needs the block for the half it
//     declared. The undeclared idempotentHint: false that comes with it is the
//     protocol's own default for a hint nobody set, so it asserts nothing a
//     conforming reader would not already have assumed.
//
// The one state that survives nowhere is a mutation declaring Idempotent
// false: on the wire that is identical to declaring nothing. Nothing here can
// fix that and no client could read the difference if it did, because the
// protocol has two states where the kernel has three.
func annotationsFor(e services.Entry) *mcp.ToolAnnotations {
	readOnly := e.Kind == services.Query
	if !readOnly && e.Idempotent == nil && e.Destructive == nil {
		return nil
	}

	a := &mcp.ToolAnnotations{ReadOnlyHint: readOnly}
	if e.Idempotent != nil {
		a.IdempotentHint = *e.Idempotent
	}
	if e.Destructive != nil {
		// Dereferenced and re-taken rather than passed straight through. This
		// annotation is published once and then read by every client that
		// lists, so sharing a pointer the caller still holds would let a later
		// write to that bool rewrite what clients were told, with nothing to
		// announce it.
		//
		// Register detaches these flags too, so this is now the second of two
		// independent guarantees rather than the only one. Keep it anyway: it
		// is what makes the property true of this function rather than true
		// only while the kernel keeps its half of the bargain, and it is the
		// thing that would catch a regression on that side.
		destructive := *e.Destructive
		a.DestructiveHint = &destructive
	}
	return a
}

// maxToolNameLen is the length limit the MCP tool-name rules impose.
const maxToolNameLen = 128

// checkName rejects a spec name that cannot serve as an MCP tool name.
//
// The SDK checks the same rule inside AddTool, but only logs the failure and
// registers the tool regardless -- which leaves a mount that looks healthy
// while serving a tool no conforming client will call. A name that cannot go
// on this wire is a configuration bug, and the kernel's position on
// configuration bugs is that they surface at wiring time, not at request time.
func checkName(name string) error {
	if name == "" || len(name) > maxToolNameLen {
		return fmt.Errorf(
			"mcpx: %q: an MCP tool name must be between 1 and %d characters",
			name, maxToolNameLen,
		)
	}
	for _, r := range name {
		if !validNameRune(r) {
			return fmt.Errorf(
				"mcpx: %q: an MCP tool name holds only letters, digits, underscore, hyphen and dot, and %s is none of those",
				name, strconv.QuoteRune(r),
			)
		}
	}
	return nil
}

func validNameRune(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r == '_' || r == '-' || r == '.'
}

// schemaTypeName reports what a schema says it is, for an error message. A
// schema may declare one type, several, or none, and the author reading the
// error has to see which of those they wrote.
func schemaTypeName(s *jsonschema.Schema) string {
	switch {
	case s == nil:
		return "no schema at all"
	case s.Type != "":
		return strconv.Quote(s.Type)
	case len(s.Types) > 0:
		return strconv.Quote(strings.Join(s.Types, " or "))
	}
	return "no type at all"
}
