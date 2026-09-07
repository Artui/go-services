package aguix_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/aguix"
	"github.com/google/jsonschema-go/jsonschema"
)

// loud is a string that asks to be rendered; shouting is the formatter that
// does it. No domain in either, so this is about the wiring.
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
	reg := services.New(resolve)
	services.MustRegister(reg, services.Spec[deps, struct{}, loudOut]{
		Name: "shout", Kind: services.Query, Description: "Say it.",
		Run: func(services.Ctx[deps], struct{}) (loudOut, error) {
			return loudOut{Name: "ada", Flat: "ada"}, nil
		},
	})
	return reg
}

// shoutResult drives one call and returns the decoded tool-result content, which
// is the string a client actually receives.
func shoutResult(t *testing.T, opts ...aguix.ToolboxOption) map[string]any {
	t.Helper()
	box, err := aguix.NewToolbox(loudRegistry(t), signedIn, opts...)
	if err != nil {
		t.Fatalf("NewToolbox: %v", err)
	}
	agent := aguix.Scripted(aguix.Rule{
		Steps: []aguix.Step{aguix.CallTool(box, "shout", json.RawMessage(`{}`))},
	})
	body := run(t, agent, oneTurn, aguix.WithOnError(func(*http.Request, error) {})).Body.String()
	for _, frame := range frames(t, body) {
		if frame["type"] != "TOOL_CALL_RESULT" {
			continue
		}
		content, ok := frame["content"].(string)
		if !ok {
			t.Fatalf("content = %T, want a string", frame["content"])
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(content), &decoded); err != nil {
			t.Fatalf("decoding %q: %v", content, err)
		}
		return decoded
	}
	t.Fatal("no TOOL_CALL_RESULT frame")
	return nil
}

func TestAToolboxWithARendererRendersTheMarkedField(t *testing.T) {
	got := shoutResult(t, aguix.WithToolboxRenderer(shouting()))

	if got["name"] != "ADA" {
		t.Errorf("name = %v, want it rendered", got["name"])
	}
	if got["flat"] != "ada" {
		t.Errorf("flat = %v, want an unmarked field untouched", got["flat"])
	}
}

// The default. One spec answers an HTTP route and an agent tool, and a toolbox
// given no renderer must not make the two disagree.
func TestAToolboxWithoutARendererIsUnchanged(t *testing.T) {
	got := shoutResult(t)

	if got["name"] != "ada" {
		t.Errorf("name = %v, want the raw value", got["name"])
	}
}

// A formatter that fails ends the run rather than streaming a half-rendered
// result. A client shown a tool result it cannot parse has no way to tell that
// from an empty answer, which is the same reasoning that makes an unencodable
// value the one case worth stopping for.
func TestARenderFailureEndsTheRun(t *testing.T) {
	failing := services.NewRenderer(map[string]services.Formatter{
		"shout": func(context.Context, any, any) (any, error) {
			return nil, errShout("no formatter for this branch")
		},
	})
	box, err := aguix.NewToolbox(loudRegistry(t), signedIn, aguix.WithToolboxRenderer(failing))
	if err != nil {
		t.Fatalf("NewToolbox: %v", err)
	}
	var reported error
	agent := aguix.Scripted(aguix.Rule{
		Steps: []aguix.Step{aguix.CallTool(box, "shout", json.RawMessage(`{}`))},
	})

	body := run(t, agent, oneTurn,
		aguix.WithOnError(func(_ *http.Request, e error) { reported = e })).Body.String()

	if reported == nil || !strings.Contains(reported.Error(), "no formatter for this branch") {
		t.Errorf("reported = %v, want the real reason", reported)
	}
	if strings.Contains(body, "no formatter for this branch") {
		t.Errorf("the client was told the internal reason:\n%s", body)
	}
}
