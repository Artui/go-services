package example

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/v2/agent"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"github.com/Artui/go-services/aguix"
	"github.com/Artui/go-services/httpx"
	"github.com/Artui/go-services/mcpx"
)

// What each transport actually puts in front of a model, byte for byte.
//
// The sibling Python library marks every output field with an audience --
// content a model may read aloud, a label, an opaque handle, or nothing -- and
// shapes the payload per transport from those markings. This file is the
// measurement that decides whether this library owes the same thing, and it is
// written as bytes rather than as an argument because the previous answer here
// was "no friction was recorded", from a module whose entire domain was int64
// and string. A testbed with no enum, no timestamp, no money and no opaque
// token cannot record friction about any of them.
//
// So the domain now has all four, and every expectation below is the literal
// answer a client receives. If a transport starts shaping its output, or stops
// advertising what it advertises today, one of these fails with the wrong
// string printed next to the right one.
//
// The clock is stopped for the same reason the bodies are spelled out: a due
// date and a fine are computed, and a payload nobody can predict is a payload
// nobody can pin.

// audienceDB is the seeded world with the clock stopped.
func audienceDB(t *testing.T) *sql.DB {
	t.Helper()
	return newDB(t)
}

// callMCPX makes one tool call over a real MCP session and returns everything
// the client received: the text block, the structured content, and the output
// schema the tool was advertised with.
func callMCPX(t *testing.T, db *sql.DB, tool string, args map[string]any) (string, string, string) {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: "library", Version: "v0"}, nil)
	if err := mcpx.Mount(server, registryAt(db),
		func(context.Context, *mcp.CallToolRequest) (any, error) {
			return int64(1), nil
		}); err != nil {
		t.Fatalf("mcpx mount: %v", err)
	}
	clientT, serverT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), serverT, nil); err != nil {
		t.Fatalf("mcp server connect: %v", err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil).
		Connect(t.Context(), clientT, nil)
	if err != nil {
		t.Fatalf("mcp client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("mcp list tools: %v", err)
	}
	var schema string
	for _, published := range listed.Tools {
		if published.Name == tool {
			schema = encode(t, published.OutputSchema)
		}
	}

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("mcp protocol error: %v", err)
	}
	if res.IsError {
		t.Fatalf("mcp refused %s: %v", tool, res.Content)
	}

	var text strings.Builder
	for _, block := range res.Content {
		if content, ok := block.(*mcp.TextContent); ok {
			text.WriteString(content.Text)
		}
	}
	return text.String(), encode(t, res.StructuredContent), schema
}

// callADKX runs one tool the way ADK runs it, and returns the map the model is
// handed alongside the response schema it was declared with.
func callADKX(t *testing.T, db *sql.DB, tool string, args map[string]any) (string, string) {
	t.Helper()

	ts, err := adkx.Toolset(registryAt(db), func(ctx agent.Context) (any, error) {
		return strconv.ParseInt(ctx.UserID(), 10, 64)
	})
	if err != nil {
		t.Fatalf("adkx toolset: %v", err)
	}
	ctx := &adkContext{StrictContextMock: agent.NewStrictContextMock(t.Context()), member: 1}
	published, err := ts.Tools(ctx)
	if err != nil {
		t.Fatalf("adkx tools: %v", err)
	}

	for _, one := range published {
		if one.Name() != tool {
			continue
		}
		runnable, ok := one.(adkx.RunnableTool)
		if !ok {
			t.Fatalf("adkx published %s in a shape ADK cannot run: %T", tool, one)
		}
		out, err := runnable.Run(ctx, args)
		if err != nil {
			t.Fatalf("adkx run %s: %v", tool, err)
		}
		return encode(t, out), encode(t, runnable.Declaration().ResponseJsonSchema)
	}
	t.Fatalf("adkx published no %s", tool)
	return "", ""
}

// callAGUIX drives a run the way the browser does and returns the tool result's
// content, plus the whole definition the agent side is given for that tool.
func callAGUIX(t *testing.T, db *sql.DB, said, tool string) (string, string) {
	t.Helper()

	toolbox, err := aguix.NewToolbox(registryAt(db), func(context.Context) (any, error) {
		return int64(1), nil
	})
	if err != nil {
		t.Fatalf("NewToolbox: %v", err)
	}
	handler, err := aguix.Handler(Librarian(toolbox))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	body := fmt.Sprintf(
		`{"threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":%q}]}`, said)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(body)))

	var content string
	for _, block := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n") {
		var event map[string]any
		if err := json.Unmarshal(
			[]byte(strings.TrimPrefix(block, "data: ")), &event); err != nil {
			t.Fatalf("frame is not JSON: %q", block)
		}
		if event["type"] == "TOOL_CALL_RESULT" {
			content = fmt.Sprint(event["content"])
		}
	}
	if content == "" {
		t.Fatalf("the run carried no tool result: %s", rec.Body.String())
	}

	defs, err := toolbox.Definitions()
	if err != nil {
		t.Fatalf("Definitions: %v", err)
	}
	for _, def := range defs {
		if def.Name == tool {
			return content, encode(t, def)
		}
	}
	t.Fatalf("aguix published no %s", tool)
	return "", ""
}

// callHTTPX is the browser's answer, for contrast. The claim under test is that
// an agent should be served a differently shaped payload than a browser, so the
// browser's payload has to be in the record too.
func callHTTPX(t *testing.T, db *sql.DB, path string) string {
	t.Helper()

	mux := http.NewServeMux()
	err := httpx.Mount(mux, registryAt(db), map[string]httpx.Route{
		"list_books": {Method: "GET", Pattern: "/books"},
		"list_loans": {Method: "GET", Pattern: "/loans"},
	}, headerPrincipal)
	if err != nil {
		t.Fatalf("httpx mount: %v", err)
	}

	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set(memberHeader, "1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
	}
	return strings.TrimSpace(rec.Body.String())
}

// encode renders whatever a transport handed back as the JSON it would be, so
// two transports' answers can be compared as strings.
func encode(t *testing.T, v any) string {
	t.Helper()
	if v == nil {
		return ""
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(raw)
}

// The captured payloads. Every one of these is a literal, and the point of the
// literal is that a person can read it and ask whether a model reading it aloud
// would say something wrong.
//
// There are two spellings of one value, and the split is not about audience. A
// transport that hands over the JSON the encoder produced keeps the struct's
// field order; one that hands over a Go map -- mcpx's StructuredContent, adkx's
// required map result -- has been through a type whose iteration order is
// sorted. Same fields, same values, same encoding of each; only the order
// differs, and no reader of either is told anything the other is not.
const (
	loansWire = `{"loans":[` +
		`{"loan_id":1,"book_id":11,"title":"Structure and Interpretation",` +
		`"status":"overdue","due_at":"2026-08-15T09:00:00Z","fine_cents":550},` +
		`{"loan_id":2,"book_id":10,"title":"The Mythical Man-Month",` +
		`"status":"returned","due_at":"2026-07-15T09:00:00Z","fine_cents":125}` +
		`]}`

	loansMapped = `{"loans":[` +
		`{"book_id":11,"due_at":"2026-08-15T09:00:00Z","fine_cents":550,"loan_id":1,` +
		`"status":"overdue","title":"Structure and Interpretation"},` +
		`{"book_id":10,"due_at":"2026-07-15T09:00:00Z","fine_cents":125,"loan_id":2,` +
		`"status":"returned","title":"The Mythical Man-Month"}` +
		`]}`

	pageWire = `{"books":[{"id":10,"title":"The Mythical Man-Month",` +
		`"author":"Brooks","available":2}],"next_cursor":"YWZ0ZXI6MTA"}`

	pageMapped = `{"books":[{"author":"Brooks","available":2,"id":10,` +
		`"title":"The Mythical Man-Month"}],"next_cursor":"YWZ0ZXI6MTA"}`
)

// Every agent transport serves the same bytes, and they are the bytes the
// browser gets.
//
// That is the finding, stated as an assertion so it cannot quietly stop being
// true. Nothing in this repository shapes an output for its audience: the model
// and the browser are handed one encoding of one value.
func TestEveryTransportServesTheSameLoanPayload(t *testing.T) {
	mcpText, mcpStructured, _ := callMCPX(t, audienceDB(t), "list_loans",
		map[string]any{"include_returned": true})
	adkResult, _ := callADKX(t, audienceDB(t), "list_loans",
		map[string]any{"include_returned": true})
	aguiContent, _ := callAGUIX(t, audienceDB(t), "my loans", "list_loans")
	browser := callHTTPX(t, audienceDB(t), "/loans?include_returned=true")

	for name, got := range map[string]string{
		"mcpx text":     mcpText,
		"aguix content": aguiContent,
		"httpx body":    browser,
	} {
		if got != loansWire {
			t.Errorf("%s =\n  %s\nwant\n  %s", name, got, loansWire)
		}
	}
	for name, got := range map[string]string{
		"mcpx structured": mcpStructured,
		"adkx result":     adkResult,
	} {
		if got != loansMapped {
			t.Errorf("%s =\n  %s\nwant\n  %s", name, got, loansMapped)
		}
	}
}

// The paged catalogue, which is where the opaque token lives.
//
// next_cursor is the one field in this domain that is genuinely not for a
// reader: it is a token the caller passes back, and what it encodes is this
// service's business. It is served to a model exactly as it is served to a
// browser, with nothing but its name and its schema description to say what it
// is for.
func TestEveryTransportServesTheSameOpaqueCursor(t *testing.T) {
	mcpText, mcpStructured, _ := callMCPX(t, audienceDB(t), "list_books",
		map[string]any{"limit": 1})
	adkResult, _ := callADKX(t, audienceDB(t), "list_books", map[string]any{"limit": 1})
	browser := callHTTPX(t, audienceDB(t), "/books?limit=1")

	for name, got := range map[string]string{
		"mcpx text":  mcpText,
		"httpx body": browser,
	} {
		if got != pageWire {
			t.Errorf("%s =\n  %s\nwant\n  %s", name, got, pageWire)
		}
	}
	for name, got := range map[string]string{
		"mcpx structured": mcpStructured,
		"adkx result":     adkResult,
	} {
		if got != pageMapped {
			t.Errorf("%s =\n  %s\nwant\n  %s", name, got, pageMapped)
		}
	}
}

// A cursor handed back fetches the page after the one it came from, which is
// the whole reason the token has to survive the round trip through a model.
func TestACursorFetchesTheNextPage(t *testing.T) {
	db := audienceDB(t)
	res, err := registryAt(db).Dispatch(t.Context(), int64(1), "list_books",
		json.RawMessage(`{"limit":1,"cursor":"YWZ0ZXI6MTA"}`))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	out := res.Value.(ListOut)
	if len(out.Books) != 1 || out.Books[0].ID != 11 {
		t.Fatalf("books = %v, want only book 11", out.Books)
	}
}

// What each transport tells a model about the OUTPUT, which is the only channel
// in this library through which a field could say "I am not for reading aloud".
//
// The three answers differ, and the difference is the finding: two of the three
// carry the description a spec author wrote on the field, and the third
// publishes no output schema at all.
func TestOnlySomeTransportsAdvertiseTheOutputSchema(t *testing.T) {
	_, _, mcpSchema := callMCPX(t, audienceDB(t), "list_books", map[string]any{"limit": 1})
	_, adkSchema := callADKX(t, audienceDB(t), "list_books", map[string]any{"limit": 1})
	_, aguiDefinition := callAGUIX(t, audienceDB(t), "show me the books", "list_books")

	// The words the field carries. They are the author's, they are not derived
	// from anything, and they are the only thing that distinguishes this field
	// from an identifier a reader may quote.
	const said = "an opaque token; pass it back as cursor to fetch the next page, " +
		"and do not show it to a person or try to read it"

	if !strings.Contains(mcpSchema, said) {
		t.Errorf("mcpx advertised an output schema without the field's own words:\n  %s", mcpSchema)
	}
	if !strings.Contains(adkSchema, said) {
		t.Errorf("adkx advertised an output schema without the field's own words:\n  %s", adkSchema)
	}
	// aguix publishes Name, Description and Parameters, and nothing about the
	// output. A description written on an output field cannot reach a model on
	// this transport, whatever it says.
	if strings.Contains(aguiDefinition, said) {
		t.Errorf("aguix now carries the output field's words; the finding has changed:\n  %s",
			aguiDefinition)
	}
	// It does mention next_cursor, and only because the INPUT field's own
	// description names it. That is the author writing the same fact twice, on
	// the one field this transport publishes, and it is the whole of what an
	// AG-UI model is told about the token it will be handed back.
	if !strings.Contains(aguiDefinition,
		`"cursor":{"type":"string","description":"the next_cursor of a previous answer, passed back unchanged"}`) {
		t.Errorf("aguix no longer carries the input field's words:\n  %s", aguiDefinition)
	}
}

// A borrow answers with a timestamp and an enum, on every transport, with no
// unit and no timezone choice made for the reader beyond the encoding.
func TestABorrowServesATimestampAndAnEnum(t *testing.T) {
	const want = `{"loan_id":3,"book_id":10,"member_id":1,"remaining":1,` +
		`"status":"on_loan","due_at":"2026-09-20T12:00:00Z"}`

	const mapped = `{"book_id":10,"due_at":"2026-09-20T12:00:00Z","loan_id":3,` +
		`"member_id":1,"remaining":1,"status":"on_loan"}`

	mcpText, _, _ := callMCPX(t, audienceDB(t), "borrow_book", map[string]any{"book_id": 10})
	adkResult, _ := callADKX(t, audienceDB(t), "borrow_book", map[string]any{"book_id": 10})
	aguiContent, _ := callAGUIX(t, audienceDB(t), "borrow book 10", "borrow_book")

	for name, got := range map[string]string{
		"mcpx text":     mcpText,
		"aguix content": aguiContent,
	} {
		if got != want {
			t.Errorf("%s =\n  %s\nwant\n  %s", name, got, want)
		}
	}
	if adkResult != mapped {
		t.Errorf("adkx result =\n  %s\nwant\n  %s", adkResult, mapped)
	}
}

// A marking is expressible today, and this is the probe that proves it.
//
// probeToken is not part of the library. It exists to answer one question: if
// this repository wanted a machine-readable "this field is a handle, not
// something to read out", is a kernel change what it would cost? The kernel's
// SchemaFor lets any named type declare its own schema, jsonschema.Schema.Extra
// carries keywords the vocabulary does not name, and both halves survive to a
// client -- so the answer is that the channel already exists and only a
// vocabulary would be new.
type probeToken string

// JSONSchema declares the marking the way a marking would have to be declared.
func (probeToken) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{
		Type:        "string",
		Description: "an opaque token",
		Extra:       map[string]any{"x-audience": "handle"},
	}, nil
}

type probeIn struct{}

type probeOut struct {
	Token probeToken `json:"token"`
}

// TestAFieldMarkingReachesTheWireWithNoKernelChange registers a spec whose
// output carries the marking, mounts it, and reads what a client is told.
func TestAFieldMarkingReachesTheWireWithNoKernelChange(t *testing.T) {
	db := audienceDB(t)
	reg := services.New(resolverOver(db))
	services.MustRegister(reg, services.Spec[Deps, probeIn, probeOut]{
		Name: "probe", Kind: services.Query,
		Run: func(services.Ctx[Deps], probeIn) (probeOut, error) {
			return probeOut{Token: "YWZ0ZXI6MTA"}, nil
		},
	})

	server := mcp.NewServer(&mcp.Implementation{Name: "probe", Version: "v0"}, nil)
	if err := mcpx.Mount(server, reg,
		func(context.Context, *mcp.CallToolRequest) (any, error) {
			return int64(1), nil
		}); err != nil {
		t.Fatalf("mcpx mount: %v", err)
	}
	clientT, serverT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), serverT, nil); err != nil {
		t.Fatalf("mcp server connect: %v", err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil).
		Connect(t.Context(), clientT, nil)
	if err != nil {
		t.Fatalf("mcp client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("mcp list tools: %v", err)
	}
	if len(listed.Tools) != 1 {
		t.Fatalf("listed %d tools, want the probe", len(listed.Tools))
	}
	schema := encode(t, listed.Tools[0].OutputSchema)

	const want = `{"additionalProperties":false,"properties":{"token":` +
		`{"description":"an opaque token","type":"string","x-audience":"handle"}},` +
		`"required":["token"],"type":"object"}`
	if schema != want {
		t.Errorf("output schema =\n  %s\nwant\n  %s", schema, want)
	}
}
