package adkx_test

import (
	"errors"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// runnableTool is ADK's own dispatch shape, restated here because the real one
// is unexported.
//
// That is the whole risk this package carries: ADK matches a tool structurally
// at the point of use, so nothing in the compiler tells us a method drifted.
// Asserting the shape in a test is what turns a runtime surprise into a build
// failure, and it is why the interface is written out rather than inferred.
type runnableTool interface {
	tool.Tool
	Declaration() *genai.FunctionDeclaration
	Run(ctx agent.Context, args any) (map[string]any, error)
}

func mustToolset(t *testing.T, opts ...adkx.Option) tool.Toolset {
	t.Helper()
	return mustToolsetOf(t, newRegistry(t), opts...)
}

// mustToolsetOf publishes a registry the caller already holds, for the tests
// that need to compare what the toolset carries against what the registry
// reflected. Building a second registry there compares two schemas rather than
// one object, which is the opposite of what the identity test is for.
func mustToolsetOf(
	t *testing.T, reg *services.Registry[deps], opts ...adkx.Option,
) tool.Toolset {
	t.Helper()
	ts, err := adkx.Toolset(reg, adkx.UserID, opts...)
	if err != nil {
		t.Fatalf("Toolset: %v", err)
	}
	return ts
}

func toolNamed(t *testing.T, ts tool.Toolset, name string) runnableTool {
	t.Helper()
	tools, err := ts.Tools(contextFor(t, "ada"))
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	for _, candidate := range tools {
		if candidate.Name() != name {
			continue
		}
		// The structural assertion, made where it can fail a build.
		runnable, ok := candidate.(runnableTool)
		if !ok {
			t.Fatalf("%q does not satisfy ADK's runnable tool shape: %T", name, candidate)
		}
		return runnable
	}
	t.Fatalf("no tool named %q", name)
	return nil
}

func TestEverySpecIsPublished(t *testing.T) {
	reg := newRegistry(t)
	tools, err := mustToolsetOf(t, reg).Tools(contextFor(t, "ada"))
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != len(reg.Entries()) {
		t.Errorf("published %d tools, want one per spec", len(tools))
	}
	for _, published := range tools {
		if _, ok := published.(runnableTool); !ok {
			t.Errorf("%q does not satisfy ADK's runnable tool shape", published.Name())
		}
	}
}

// The claim this package exists to make: the schema a model is shown is the
// object the kernel already reflected, not a second derivation of it.
func TestTheDeclarationCarriesTheKernelsOwnSchema(t *testing.T) {
	reg := newRegistry(t)
	entry, ok := reg.Lookup("borrow_book")
	if !ok {
		t.Fatal("borrow_book is not registered")
	}

	decl := toolNamed(t, mustToolsetOf(t, reg), "borrow_book").Declaration()
	if decl.Name != "borrow_book" {
		t.Errorf("Name = %q", decl.Name)
	}
	if decl.Description == "" {
		t.Error("Description is empty; the spec declares one")
	}
	// Pointer identity, not equality. Two schemas that agree today are two
	// things that can stop agreeing; one object cannot.
	if decl.ParametersJsonSchema != any(entry.Input) {
		t.Error("ParametersJsonSchema is not the kernel's own schema object")
	}
}

func TestASuccessIsRenderedAsAMap(t *testing.T) {
	got, err := toolNamed(t, mustToolset(t), "borrow_book").
		Run(contextFor(t, "ada"), map[string]any{"book_id": 4})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got["by"] != "ada" {
		t.Errorf("by = %v, want the principal to have reached Deps", got["by"])
	}
	if got["loan_id"] == nil {
		t.Error("loan_id missing")
	}
}

// A scalar output is wrapped, the way adk-go's own functiontool wraps one.
func TestAScalarOutputIsWrapped(t *testing.T) {
	got, err := toolNamed(t, mustToolset(t), "ping").Run(contextFor(t, "ada"), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got["result"] != "pong" {
		t.Errorf("result = %v, want pong", got["result"])
	}
}

// ADK renders a returned error as {"error": err.Error()} and shows it to the
// model, so what the taxonomy says is what the model reads.
func TestARefusalKeepsTheServicesOwnWords(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     map[string]any
		sentinel error
		says     string
	}{
		{"permission", map[string]any{"book_id": 13}, services.ErrPermission, "book 13 is reference only"},
		{"not found", map[string]any{"book_id": 99}, services.ErrNotFound, "no book 99"},
		{"conflict", map[string]any{"book_id": 7}, services.ErrConflict, "no copy on the shelf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := toolNamed(t, mustToolset(t), "borrow_book").
				Run(contextFor(t, "ada"), tc.args)
			if err == nil {
				t.Fatal("Run succeeded, want a refusal")
			}
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("err = %v, want %v", err, tc.sentinel)
			}
			if !contains(err.Error(), tc.says) {
				t.Errorf("err = %v, want it to keep the service's words", err)
			}
			// And no package name, since kernel v0.4.0.
			if contains(err.Error(), "services:") {
				t.Errorf("err = %v names the implementation's package", err)
			}
		})
	}
}

// A validation failure is rendered per field, because the model's next move is
// to correct an argument.
func TestAValidationFailureTellsTheModelWhatToChange(t *testing.T) {
	_, err := toolNamed(t, mustToolset(t), "borrow_book").
		Run(contextFor(t, "ada"), map[string]any{"book_id": 0})
	if err == nil {
		t.Fatal("Run succeeded, want a refusal")
	}
	if !contains(err.Error(), "book_id: must be a positive identifier") {
		t.Errorf("err = %v, want the field and its message", err)
	}
	if !contains(err.Error(), "call the tool again") {
		t.Errorf("err = %v, want it to say retrying is expected", err)
	}
}

// Description and IsLongRunning are what ADK reads to build the declaration and
// to decide whether the agent loop waits for a second event. Asserted because a
// method nothing calls is a method nothing notices breaking.
func TestToolMetadata(t *testing.T) {
	borrow := toolNamed(t, mustToolset(t), "borrow_book")

	if borrow.Description() == "" {
		t.Error("Description is empty; the spec declares one")
	}
	// A services.Spec returns its result. Claiming otherwise would tell the
	// agent loop to expect a completion event that never arrives.
	if borrow.IsLongRunning() {
		t.Error("IsLongRunning is true; no spec is a long-running operation")
	}
}

// ADK hands every tool a map, and anything else is this deployment being wrong
// rather than the model being wrong -- so it is ErrConfiguration and redacted,
// not an argument problem the model could fix by trying again.
func TestArgumentsThatAreNotAMapAreAConfigurationFault(t *testing.T) {
	_, err := toolNamed(t, mustToolset(t), "ping").Run(contextFor(t, "ada"), "not a map")
	if err == nil {
		t.Fatal("Run accepted arguments that were not a map")
	}
	if err.Error() != adkx.InternalErrorText {
		t.Errorf("the model was told %q, want the fixed sentence", err)
	}
}
