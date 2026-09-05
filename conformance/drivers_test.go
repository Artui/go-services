package conformance_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/conformance"
	"github.com/Artui/go-services/ginx"
	"github.com/Artui/go-services/httpx"
	"github.com/Artui/go-services/mcpx"
	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/v2/agent"
	adktool "google.golang.org/adk/v2/tool"
	"google.golang.org/genai"

	"github.com/Artui/go-services/adkx"
)

// The route table, shared so the two HTTP adapters are mounted identically.
// Only the path syntax differs, which is the one thing they cannot share.
//
// location is the Location template, shared for the same reason. The two
// adapters build the header at different points -- one expands before
// marshalling and clears it if the marshal fails, the other marshals first and
// never expands -- so mounting them with the same template is what turns that
// into a comparison instead of two separate opinions.
var routes = []struct {
	spec, method, stdPattern, ginPath, location string
}{
	{"create_author", "POST", "/authors", "/authors", "/authors/{name}"},
	{"get_author", "GET", "/authors/{name}", "/authors/:name", ""},
	{"patch_author", "PATCH", "/authors", "/authors", ""},
	{"no_args", "GET", "/ping", "/ping", ""},
	{"denied", "POST", "/denied", "/denied", ""},
	{"missing", "POST", "/missing", "/missing", ""},
	{"clash", "POST", "/clash", "/clash", ""},
	{"boom", "POST", "/boom", "/boom", ""},
	{"typed_nil", "POST", "/typed_nil", "/typed_nil", ""},
	// A fixed template over a value that cannot be encoded. This is the case
	// where the two adapters take genuinely different paths to the same answer,
	// and neither may end up claiming something was created.
	{"unencodable", "GET", "/unencodable", "/unencodable", "/unencodable/fixed"},
}

func anonymousHTTP(*http.Request) (any, error)                        { return nil, nil }
func anonymousGin(*gin.Context) (any, error)                          { return nil, nil }
func anonymousMCP(context.Context, *mcp.CallToolRequest) (any, error) { return nil, nil }

func mountHTTPX(t *testing.T, reg *services.Registry[conformance.Deps]) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	table := map[string]httpx.Route{}
	for _, r := range routes {
		table[r.spec] = httpx.Route{
			Method: r.method, Pattern: r.stdPattern, Location: r.location,
		}
	}
	if err := httpx.Mount(mux, reg, table, anonymousHTTP); err != nil {
		t.Fatalf("httpx mount: %v", err)
	}
	return mux
}

func mountGinx(t *testing.T, reg *services.Registry[conformance.Deps]) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	table := map[string]ginx.Route{}
	for _, r := range routes {
		table[r.spec] = ginx.Route{Method: r.method, Path: r.ginPath, Location: r.location}
	}
	if err := ginx.Mount(engine, reg, table, anonymousGin); err != nil {
		t.Fatalf("ginx mount: %v", err)
	}
	return engine
}

func mountMCPX(t *testing.T, reg *services.Registry[conformance.Deps]) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "conformance", Version: "v0"}, nil)
	if err := mcpx.Mount(server, reg, anonymousMCP); err != nil {
		t.Fatalf("mcpx mount: %v", err)
	}
	clientT, serverT := mcp.NewInMemoryTransports()
	ctx := t.Context()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "conformance-client", Version: "v0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// serveHTTP drives one request and reduces the response to an Outcome.
func serveHTTP(h http.Handler, method, target, body string) conformance.Outcome {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, reader))
	raw, _ := io.ReadAll(rec.Body)

	out := conformance.Outcome{
		Status:   rec.Code,
		Failed:   rec.Code >= 400,
		Wire:     string(raw),
		Location: rec.Header().Get("Location"),
		Leaked:   strings.Contains(string(raw), conformance.SecretText),
	}
	if out.Failed {
		out.Messages = conformance.MessagesFromJSON(raw)
		return out
	}
	_ = json.Unmarshal(raw, &out.Value)
	return out
}

// callTool drives the same operation over MCP and reduces it the same way.
func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) conformance.Outcome {
	t.Helper()
	res, err := s.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		// A protocol error, which is a different thing from a refusal and is
		// never the right answer for a service that declined to run.
		t.Fatalf("%s: protocol error, want a tool result: %v", name, err)
	}

	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	out := conformance.Outcome{
		Failed: res.IsError,
		Wire:   text.String(),
		Leaked: strings.Contains(text.String(), conformance.SecretText),
	}
	if out.Failed {
		out.Messages = conformance.MessagesFromText(text.String())
		return out
	}
	if res.StructuredContent != nil {
		encoded, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(encoded, &out.Value)
	}
	return out
}

// adkTool is ADK's own dispatch shape, restated here because the real one is
// unexported. It is the same assertion adkx's own suite makes, and it is worth
// repeating: ADK matches a tool structurally at the point of use, so a drifted
// method is a runtime surprise unless something names the shape.
type adkTool interface {
	adktool.Tool
	Declaration() *genai.FunctionDeclaration
	Run(ctx agent.Context, args any) (map[string]any, error)
}

// adkContext is an invocation context for the anonymous principal this suite
// uses everywhere. StrictContextMock is ADK's own double and implements the
// whole surface, so this keeps compiling as those interfaces grow.
type adkContext struct {
	agent.StrictContextMock
}

func (c *adkContext) UserID() string { return "" }

func mountADKX(t *testing.T, reg *services.Registry[conformance.Deps]) map[string]adkTool {
	t.Helper()
	ts, err := adkx.Toolset(reg, adkx.Anonymous)
	if err != nil {
		t.Fatalf("adkx toolset: %v", err)
	}
	published, err := ts.Tools(&adkContext{StrictContextMock: agent.NewStrictContextMock(t.Context())})
	if err != nil {
		t.Fatalf("adkx tools: %v", err)
	}

	tools := map[string]adkTool{}
	for _, one := range published {
		runnable, ok := one.(adkTool)
		if !ok {
			t.Fatalf("adkx published %q in a shape ADK cannot run: %T", one.Name(), one)
		}
		tools[one.Name()] = runnable
	}
	return tools
}

// What this leg does NOT cover, which matters because a green suite is easy to
// over-read.
//
// In production, ADK hands a tool genai.FunctionCall.Args, a map[string]any it
// decoded from the model's reply -- so an integer past 2^53 is already a
// float64 before adkx is reached. This harness calls Run directly with the case
// table's own map, which carries an int64, so the "identifier past 2^53" case
// passes here and would not pass through a real ADK invocation.
//
// That is a limit of driving the tool rather than the framework, and it is
// written down instead of fixed because fixing it means standing up an agent
// and a model to reproduce a rounding that adkx cannot prevent anyway. The
// package documents the same thing for its users. What this leg does prove is
// everything downstream of the arguments: the dispatch, the taxonomy, the
// redaction, and that the result agrees with the other three.

// callADK drives the same operation through ADK and reduces it the same way.
//
// The value is put back through the JSON encoder before it is compared. adkx
// decodes its result with UseNumber, so its map holds json.Number where the
// other drivers hold float64 -- a difference in how this harness reads the
// answer rather than in the answer, since ADK marshals the map onward either
// way. Normalising here is what keeps the comparison about the transports.
func callADK(t *testing.T, tools map[string]adkTool, name string, args map[string]any) conformance.Outcome {
	t.Helper()
	one, ok := tools[name]
	if !ok {
		t.Fatalf("adkx published no tool named %q", name)
	}

	ctx := &adkContext{StrictContextMock: agent.NewStrictContextMock(t.Context())}
	result, err := one.Run(ctx, args)
	if err != nil {
		// ADK renders this as map[string]any{"error": err.Error()} and shows it
		// to the model, so the error's text is this transport's wire.
		text := err.Error()
		return conformance.Outcome{
			Failed:   true,
			Wire:     text,
			Messages: conformance.MessagesFromText(text),
			Leaked:   strings.Contains(text, conformance.SecretText),
		}
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("adkx returned a result that cannot be encoded: %v", err)
	}
	out := conformance.Outcome{
		Wire:   string(raw),
		Leaked: strings.Contains(string(raw), conformance.SecretText),
	}
	if err := json.Unmarshal(raw, &out.Value); err != nil {
		t.Fatalf("adkx result is not an object: %v", err)
	}
	return out
}
