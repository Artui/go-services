package adkx

import (
	"fmt"

	services "github.com/Artui/go-services"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/genai"
)

// specTool is one registered spec published as an ADK tool.
//
// It satisfies ADK's internal FunctionTool shape structurally -- Name,
// Description, IsLongRunning, Declaration and Run. That interface is
// unexported, so this is checked by ADK at the point of use rather than by the
// compiler here; the toolset's tests call through a real agent context for that
// reason.
type specTool[D any] struct {
	reg       *services.Registry[D]
	entry     services.Entry
	principal Principal
	owner     *toolset
	decl      *genai.FunctionDeclaration
}

// newTool builds one.
//
// It returns no error, deliberately. Everything that could be wrong about an
// entry -- a missing name, an unreflectable type, a duplicate -- was refused by
// Register before the entry existed, so a validation pass here would be a
// second opinion about a fact the kernel already settled, and its failure arm
// would be a statement no test could ever reach.
func newTool[D any](
	reg *services.Registry[D], entry services.Entry, principal Principal, owner *toolset,
) *specTool[D] {
	// The whole argument for this package, in one assignment. ParametersJsonSchema
	// takes the *jsonschema.Schema the kernel reflected at registration, so the
	// schema a model is shown and the schema the kernel enforces are the same
	// object rather than two derivations of one struct. adk-go's own
	// functiontool fills this field the same way, from the same library at the
	// same version.
	decl := &genai.FunctionDeclaration{
		Name:                 entry.Name,
		Description:          entry.Description,
		ParametersJsonSchema: entry.Input,
	}
	if entry.Output != nil {
		decl.ResponseJsonSchema = entry.Output
	}

	return &specTool[D]{
		reg: reg, entry: entry, principal: principal, owner: owner, decl: decl,
	}
}

func (t *specTool[D]) Name() string        { return t.entry.Name }
func (t *specTool[D]) Description() string { return t.entry.Description }

// IsLongRunning is false for every spec.
//
// ADK means something specific by it: a tool that returns a handle now and
// finishes later, which the agent loop must not wait on. A services.Spec is a
// function that returns its result, so claiming otherwise would tell the loop
// to expect a second event that never arrives.
func (t *specTool[D]) IsLongRunning() bool { return false }

// Declaration is what the model is shown.
func (t *specTool[D]) Declaration() *genai.FunctionDeclaration { return t.decl }

// Run authenticates, dispatches, and renders the answer as the map ADK requires.
//
// Everything that matters -- validation, the permission layer, the transaction
// boundary, the error taxonomy -- is below Dispatch, where a fourth adapter
// cannot forget it.
func (t *specTool[D]) Run(ctx agent.Context, args any) (map[string]any, error) {
	who, err := t.principal(ctx)
	if err != nil {
		return t.fail(ctx, err)
	}

	// ADK hands every tool a map, so this is the one adapter that cannot use
	// Dispatch. genai.FunctionCall.Args is a map[string]any and the model's
	// arguments were decoded into it before this package was reached, which is
	// why DispatchValue's warning about numbers is documented on the package
	// rather than worked around here: the rounding has already happened.
	values, ok := args.(map[string]any)
	if !ok {
		if args != nil {
			return t.fail(ctx, fmt.Errorf(
				"%w: adkx: expected arguments as a map, got %T", services.ErrConfiguration, args))
		}
		// A tool taking no arguments is called with nothing at all.
		values = map[string]any{}
	}

	result, err := t.reg.DispatchValue(ctx, who, t.entry.Name, values)
	if err != nil {
		return t.fail(ctx, err)
	}

	rendered, err := succeed(result.Value)
	if err != nil {
		// The service returned something no encoder can represent. That is this
		// process's bug rather than the model's, so it is redacted and reported
		// like any other unexpected failure.
		return t.fail(ctx, err)
	}
	return rendered, nil
}

// fail renders a failure for the model and reports the ones it redacted.
func (t *specTool[D]) fail(ctx agent.Context, err error) (map[string]any, error) {
	answer, known := refuse(err)
	if !known {
		t.owner.observe(ctx, t.entry.Name, err)
	}
	return nil, answer
}
