package example

// A prototype of the rendering mechanism, built here before it is proposed for
// the kernel, because every other claim in this module was measured before it
// was believed.
//
// The problem it solves is one this module already recorded and could not close:
// a description is declared once, at registration, with no reader in front of it
// -- so it can say "in cents of US dollars" and can never say "half past two on
// Tuesday", because the second is a fact about who is asking. A struct tag has
// the identical limit for the identical reason, which is why the earlier finding
// concluded that a rendering *tag* was not the answer.
//
// The split that works is to put each half where it belongs. The declaration
// stays a fact about the FIELD: `x-render: "money-minor"` says this integer is
// money in minor units, which is true of every row and every reader. The
// formatter is a fact about the READER: it runs adapter-side, per call, with the
// principal in hand, and it is what knows the currency and the timezone.
//
// Neither half touches Deps, which is what makes this legal at all. Deps is
// resolved inside the transaction and its handle is dead by the time a Result
// exists -- so a renderer that reached for it would be reaching into a closed
// transaction. It does not need to: an adapter already holds ctx and the
// principal, because it passed them to Dispatch.

import (
	"context"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// RenderKeyword is the schema extension a field uses to ask for formatting.
//
// It is spelt as an `x-` keyword because that is what JSON Schema reserves for
// vocabulary it does not define, and it rides on jsonschema.Schema.Extra, which
// this module already proved reaches a real MCP client unaltered.
const RenderKeyword = "x-render"

// A Formatter turns one declared value into what a particular reader should be
// shown. It is handed the principal rather than Deps, deliberately: Deps belongs
// to a transaction that has already closed.
type Formatter func(ctx context.Context, principal any, value any) (any, error)

// A Renderer applies formatters to a decoded JSON value, guided by the schema
// the kernel reflected for it.
//
// It walks the value and the schema together rather than the value alone,
// because the instruction is on the schema: a bare 550 says nothing, and
// `{"type":"integer","x-render":"money-minor"}` beside it is the whole
// difference between an integer and an amount of money.
type Renderer struct {
	formatters map[string]Formatter
}

// NewRenderer returns a Renderer for the named formatters. An unknown keyword is
// left alone rather than refused -- a schema may travel to a consumer that has
// never heard of that vocabulary, and dropping the value would be worse than
// showing it raw.
func NewRenderer(formatters map[string]Formatter) *Renderer {
	owned := make(map[string]Formatter, len(formatters))
	for name, f := range formatters {
		owned[name] = f
	}
	return &Renderer{formatters: owned}
}

// Render walks value against schema and returns it with every declared
// formatter applied.
//
// value is the DECODED JSON form -- maps, slices and scalars -- not the Go
// struct. An adapter has already marshalled by this point, and walking JSON is
// what lets a formatter replace an integer with a string without the Go type
// system objecting.
func (r *Renderer) Render(
	ctx context.Context, principal any, schema *jsonschema.Schema, value any,
) (any, error) {
	if schema == nil {
		return value, nil
	}

	if name, ok := renderName(schema); ok {
		formatter, known := r.formatters[name]
		if !known {
			return value, nil
		}
		formatted, err := formatter(ctx, principal, value)
		if err != nil {
			return nil, fmt.Errorf("rendering %q: %w", name, err)
		}
		return formatted, nil
	}

	switch v := value.(type) {
	case map[string]any:
		return r.renderObject(ctx, principal, schema, v)
	case []any:
		return r.renderArray(ctx, principal, schema, v)
	default:
		return value, nil
	}
}

func (r *Renderer) renderObject(
	ctx context.Context, principal any, schema *jsonschema.Schema, value map[string]any,
) (any, error) {
	// A copy, so a formatter cannot mutate the value an adapter may still hold.
	out := make(map[string]any, len(value))
	for key, field := range value {
		rendered, err := r.Render(ctx, principal, schema.Properties[key], field)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		out[key] = rendered
	}
	return out, nil
}

func (r *Renderer) renderArray(
	ctx context.Context, principal any, schema *jsonschema.Schema, value []any,
) (any, error) {
	out := make([]any, len(value))
	for i, item := range value {
		rendered, err := r.Render(ctx, principal, schema.Items, item)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		out[i] = rendered
	}
	return out, nil
}

// renderName reads the keyword off a schema, and reports absent for anything
// that is not a string -- a schema is data from a consumer, and a number under
// this key is a mistake rather than an instruction.
func renderName(schema *jsonschema.Schema) (string, bool) {
	raw, ok := schema.Extra[RenderKeyword]
	if !ok {
		return "", false
	}
	name, ok := raw.(string)
	return name, ok
}
