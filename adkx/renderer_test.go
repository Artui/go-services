package adkx_test

import (
	"context"
	"strings"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/adk/v2/agent"
)

// loud is a string that asks to be rendered, and shouting is the formatter that
// does it -- no domain in either, so this is about the wiring.
type loud string

func (loud) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{
		Type:  "string",
		Extra: map[string]any{services.RenderKeyword: "shout"},
	}, nil
}

type loudOut struct {
	Name loud   `json:"name"`
	Flat string `json:"flat"`
}

func shouting() *services.Renderer {
	return services.NewRenderer(map[string]services.Formatter{
		"shout": func(_ context.Context, _ any, v any) (any, error) {
			s, ok := v.(string)
			if !ok {
				return nil, errShout("not a string")
			}
			return strings.ToUpper(s), nil
		},
	})
}

type errShout string

func (e errShout) Error() string { return string(e) }

func loudRegistry(t *testing.T) *services.Registry[deps] {
	t.Helper()
	reg := services.New(func(context.Context, any) (deps, error) { return deps{}, nil })
	if err := services.Register(reg, services.Spec[deps, struct{}, loudOut]{
		Name: "shout", Kind: services.Query, Description: "Say it.",
		Run: func(services.Ctx[deps], struct{}) (loudOut, error) {
			return loudOut{Name: "ada", Flat: "ada"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return reg
}

func runShout(t *testing.T, opts ...adkx.Option) map[string]any {
	t.Helper()
	ts, err := adkx.Toolset(loudRegistry(t), adkx.Anonymous, opts...)
	if err != nil {
		t.Fatalf("Toolset: %v", err)
	}
	out, err := toolNamed(t, ts, "shout").Run(contextFor(t, "ada"), map[string]any{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return out
}

func TestAToolsetWithARendererRendersTheMarkedField(t *testing.T) {
	got := runShout(t, adkx.WithRenderer(shouting()))

	if got["name"] != "ADA" {
		t.Errorf("name = %v, want it rendered", got["name"])
	}
	if got["flat"] != "ada" {
		t.Errorf("flat = %v, want an unmarked field untouched", got["flat"])
	}
}

// The default. One spec answers an HTTP route and an agent tool, and a toolset
// that was given no renderer must not make the two disagree.
func TestAToolsetWithoutARendererIsUnchanged(t *testing.T) {
	got := runShout(t)

	if got["name"] != "ada" {
		t.Errorf("name = %v, want the raw value", got["name"])
	}
}

// A failing formatter takes the redaction path like any other unexpected fault,
// and the reporter is what carries the real reason.
func TestARenderFailureIsReportedAndRedacted(t *testing.T) {
	var reported error
	failing := services.NewRenderer(map[string]services.Formatter{
		"shout": func(context.Context, any, any) (any, error) {
			return nil, errShout("no formatter for this branch")
		},
	})
	ts, err := adkx.Toolset(loudRegistry(t), adkx.Anonymous,
		adkx.WithRenderer(failing),
		adkx.WithErrorReporter(func(_ agent.Context, _ string, err error) { reported = err }))
	if err != nil {
		t.Fatalf("Toolset: %v", err)
	}

	_, runErr := toolNamed(t, ts, "shout").Run(contextFor(t, "ada"), map[string]any{})

	if runErr == nil {
		t.Fatal("a failed render was not reported to the caller")
	}
	if strings.Contains(runErr.Error(), "no formatter for this branch") {
		t.Errorf("the model was told the internal reason: %v", runErr)
	}
	if reported == nil || !strings.Contains(reported.Error(), "no formatter for this branch") {
		t.Errorf("reported = %v, want the real reason", reported)
	}
}
