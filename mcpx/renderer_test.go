package mcpx_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/mcpx"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// shouting uppercases whatever the schema marks. A formatter with no domain in
// it, so the test is about the wiring rather than about money or time.
func shouting() *services.Renderer {
	return services.NewRenderer(map[string]services.Formatter{
		"shout": func(_ context.Context, _ any, v any) (any, error) {
			s, ok := v.(string)
			if !ok {
				return nil, errString("not a string")
			}
			return strings.ToUpper(s), nil
		},
	})
}

type errString string

func (e errString) Error() string { return string(e) }

// loud is a string that asks to be rendered, declared the way a real one would.
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

func loudRegistry(t *testing.T) *services.Registry[deps] {
	t.Helper()
	reg := services.New(func(context.Context, any) (deps, error) { return deps{}, nil })
	must(t, services.Register(reg, services.Spec[deps, struct{}, loudOut]{
		Name: "shout", Kind: services.Query, Description: "Say it.",
		Run: func(services.Ctx[deps], struct{}) (loudOut, error) {
			return loudOut{Name: "ada", Flat: "ada"}, nil
		},
	}))
	return reg
}

func callShout(t *testing.T, session *mcp.ClientSession) map[string]any {
	t.Helper()
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "shout"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content = %T, want text", res.Content[0])
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text.Text), &decoded); err != nil {
		t.Fatalf("decoding %q: %v", text.Text, err)
	}
	return decoded
}

func TestAMountWithARendererRendersTheMarkedField(t *testing.T) {
	session := connect(t, loudRegistry(t), nil, mcpx.WithRenderer(shouting()))

	got := callShout(t, session)

	if got["name"] != "ADA" {
		t.Errorf("name = %v, want it rendered", got["name"])
	}
	if got["flat"] != "ada" {
		t.Errorf("flat = %v, want an unmarked field untouched", got["flat"])
	}
}

// The default, and the property the whole design rests on: a mount that was not
// given a renderer serves what the service returned, so one spec can answer an
// HTTP route and an agent tool without the two disagreeing about what a field
// means.
func TestAMountWithoutARendererIsUnchanged(t *testing.T) {
	session := connect(t, loudRegistry(t), nil)

	got := callShout(t, session)

	if got["name"] != "ada" {
		t.Errorf("name = %v, want the raw value", got["name"])
	}
}

// A formatter that fails takes the internal-error path rather than answering
// with a half-rendered payload, and the reporter sees the real reason.
func TestARenderFailureIsReportedAndRedacted(t *testing.T) {
	var reported error
	failing := services.NewRenderer(map[string]services.Formatter{
		"shout": func(context.Context, any, any) (any, error) {
			return nil, errString("no formatter for this branch")
		},
	})
	session := connect(t, loudRegistry(t), nil,
		mcpx.WithRenderer(failing),
		mcpx.WithErrorReporter(func(_ context.Context, _ string, err error) { reported = err }))

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "shout"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	if !res.IsError {
		t.Error("a failed render was not marked as an error")
	}
	if reported == nil || !strings.Contains(reported.Error(), "no formatter for this branch") {
		t.Errorf("reported = %v, want the real reason", reported)
	}
	text, _ := res.Content[0].(*mcp.TextContent)
	if text != nil && strings.Contains(text.Text, "no formatter for this branch") {
		t.Errorf("the client was told the internal reason: %s", text.Text)
	}
}
