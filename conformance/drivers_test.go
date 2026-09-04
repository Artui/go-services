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
)

// The route table, shared so the two HTTP adapters are mounted identically.
// Only the path syntax differs, which is the one thing they cannot share.
var routes = []struct {
	spec, method, stdPattern, ginPath string
}{
	{"create_author", "POST", "/authors", "/authors"},
	{"get_author", "GET", "/authors/{name}", "/authors/:name"},
	{"patch_author", "PATCH", "/authors", "/authors"},
	{"no_args", "GET", "/ping", "/ping"},
	{"denied", "POST", "/denied", "/denied"},
	{"missing", "POST", "/missing", "/missing"},
	{"clash", "POST", "/clash", "/clash"},
	{"boom", "POST", "/boom", "/boom"},
	{"typed_nil", "POST", "/typed_nil", "/typed_nil"},
	{"unencodable", "GET", "/unencodable", "/unencodable"},
}

func anonymousHTTP(*http.Request) (any, error)                        { return nil, nil }
func anonymousGin(*gin.Context) (any, error)                          { return nil, nil }
func anonymousMCP(context.Context, *mcp.CallToolRequest) (any, error) { return nil, nil }

func mountHTTPX(t *testing.T, reg *services.Registry[conformance.Deps]) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	table := map[string]httpx.Route{}
	for _, r := range routes {
		table[r.spec] = httpx.Route{Method: r.method, Pattern: r.stdPattern}
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
		table[r.spec] = ginx.Route{Method: r.method, Path: r.ginPath}
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
		Status: rec.Code,
		Failed: rec.Code >= 400,
		Wire:   string(raw),
		Leaked: strings.Contains(string(raw), conformance.SecretText),
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
