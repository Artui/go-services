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
	"time"

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
// All three carry it now. They did not: aguix published a name, a description
// and the input parameters, and nothing about the output at all, so a
// description written on an output field stopped at that adapter whatever it
// said. That was finding 14, and this test is what held it -- it was written to
// assert the gap and went red the moment the gap closed, which is the only way a
// pinned finding tells you it is done.
func TestEveryTransportAdvertisesTheOutputSchema(t *testing.T) {
	_, _, mcpSchema := callMCPX(t, audienceDB(t), "list_books", map[string]any{"limit": 1})
	_, adkSchema := callADKX(t, audienceDB(t), "list_books", map[string]any{"limit": 1})
	_, aguiDefinition := callAGUIX(t, audienceDB(t), "show me the books", "list_books")

	// The words the field carries. They are the author's, they are not derived
	// from anything, and they are the only thing that distinguishes this field
	// from an identifier a reader may quote.
	const said = "an opaque token; pass it back as cursor to fetch the next page, " +
		"and do not show it to a person or try to read it"

	for name, got := range map[string]string{
		"mcpx":  mcpSchema,
		"adkx":  adkSchema,
		"aguix": aguiDefinition,
	} {
		if !strings.Contains(got, said) {
			t.Errorf("%s advertised no output schema carrying the field's own words:\n  %s", name, got)
		}
	}

	// aguix spells the key MCP's way rather than inventing a second name for the
	// same thing, because that is the vocabulary a model has already met. The
	// protocol has no field for it -- AG-UI's Tool is a name, a description and
	// parameters -- so this rides as an addition its models tolerate, the same
	// mechanism that let a tool result say it failed.
	if !strings.Contains(aguiDefinition, `"outputSchema"`) {
		t.Errorf("aguix published the schema under some other key:\n  %s", aguiDefinition)
	}

	// The input field's own description still names next_cursor. That was the
	// whole of what an AG-UI model used to be told about the token -- the author
	// writing the same fact twice, on the one field this transport published.
	// Kept because it should stay true, not because it is still load-bearing.
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

// ---------------------------------------------------------------------------
// What an annotation reaches, and where it stops. FRICTION.md finding 16.
// ---------------------------------------------------------------------------

// The words every annotated output field carries reach all three agent
// transports, and reach them identically.
//
// This is the whole of what the ANNOTATE mechanism is, stated as an assertion:
// a jsonschema struct tag on an OUTPUT field needs no library change, and the
// three transports that publish an output schema carry it verbatim. The list is
// exhaustive over the fields the annotation pass touched, so a field losing its
// words fails here by name rather than by a diff nobody reads.
func TestEveryAnnotatedOutputFieldReachesEveryAgentTransport(t *testing.T) {
	said := []string{
		"how many copies are on the shelf right now; this is a count and not a yes-or-no",
		"the book's identifier; pass it as book_id to borrow this book",
		"in cents of US dollars, so 550 means USD 5.50",
		"convert it to the reader's own timezone before stating a date or a time",
		"never null despite what the type says",
	}

	db := audienceDB(t)
	_, _, mcpBooks := callMCPX(t, db, "list_books", map[string]any{"limit": 1})
	_, _, mcpLoans := callMCPX(t, audienceDB(t), "list_loans", map[string]any{})
	_, adkBooks := callADKX(t, audienceDB(t), "list_books", map[string]any{"limit": 1})
	_, adkLoans := callADKX(t, audienceDB(t), "list_loans", map[string]any{})
	_, aguiBooks := callAGUIX(t, audienceDB(t), "show me the books", "list_books")
	_, aguiLoans := callAGUIX(t, audienceDB(t), "my loans", "list_loans")

	published := map[string]string{
		"mcpx list_books":  mcpBooks,
		"mcpx list_loans":  mcpLoans,
		"adkx list_books":  adkBooks,
		"adkx list_loans":  adkLoans,
		"aguix list_books": aguiBooks,
		"aguix list_loans": aguiLoans,
	}
	for _, words := range said {
		var found bool
		for _, schema := range published {
			if strings.Contains(schema, words) {
				found = true
			}
		}
		if !found {
			t.Errorf("no transport carries %q", words)
		}
	}

	// borrow_book's own fields, on the transport that has them.
	_, _, mcpBorrow := callMCPX(t, audienceDB(t), "borrow_book", map[string]any{"book_id": 10})
	for _, words := range []string{
		"the new loan's identifier",
		"taken from the authenticated caller and never from the request",
		"how many copies of this book are left on the shelf after this loan",
	} {
		if !strings.Contains(mcpBorrow, words) {
			t.Errorf("borrow_book's output schema lost %q:\n  %s", words, mcpBorrow)
		}
	}
}

// One output schema serves every reader, and it is one object rather than one
// per caller.
//
// This is the structural half of the timezone finding, and it is the half that
// cannot be argued with. Entries takes no principal, no context and no request;
// Output is a pointer the kernel reflected once at Register and hands out
// unchanged. So a description is a fact about a FIELD and can never be a fact
// about a READER -- not because nobody wrote the code, but because the object
// that would have to carry it is shared by everyone who asks.
//
// A struct tag that RENDERED the value instead of describing it would be read
// at exactly the same moment, from exactly the same declaration, and would have
// exactly the same reach. The fork between annotating and rendering is not what
// decides this case.
func TestOneOutputSchemaServesEveryReader(t *testing.T) {
	reg := registryAt(audienceDB(t))

	var first, second *jsonschema.Schema
	for _, e := range reg.Entries() {
		if e.Name == "list_loans" {
			first = e.Output
		}
	}
	for _, e := range reg.Entries() {
		if e.Name == "list_loans" {
			second = e.Output
		}
	}
	if first == nil || first != second {
		t.Fatalf("two reads of list_loans gave %p and %p; the schema is not one object",
			first, second)
	}

	// Two members read the same operation and are served different ROWS from the
	// one schema. That is the shape of everything this library can vary by
	// reader: the data moves and the declaration does not.
	answers := map[int64]string{
		1: `{"loans":[{"loan_id":1,"book_id":11,"title":"Structure and Interpretation",` +
			`"status":"overdue","due_at":"2026-08-15T09:00:00Z","fine_cents":550}]}`,
		2: `{"loans":[]}`,
	}
	for member, want := range answers {
		res, err := reg.Dispatch(t.Context(), member, "list_loans", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("member %d: %v", member, err)
		}
		if got := encode(t, res.Value); got != want {
			t.Errorf("member %d =\n  %s\nwant\n  %s", member, got, want)
		}
	}
}

// zonedOut is the probe that answers the timezone question rather than arguing
// it: the same instant twice, once as the service stores it and once written
// for whoever is reading.
type zonedOut struct {
	DueAt      time.Time `json:"due_at" jsonschema:"when the book must be back, RFC 3339 in UTC"`
	DueAtLocal string    `json:"due_at_local" jsonschema:"the same instant in the reader's own timezone"`
}

// probeZones stands in for a member preference the real domain does not have.
var probeZones = map[int64]string{1: "Pacific/Auckland", 2: "America/Los_Angeles"}

// A reader's timezone travels as a value or it does not travel.
//
// Two members read one loan. The schema they are shown is byte-identical -- it
// has to be, by the test above -- and the VALUES differ, because the layer that
// produced them knew who was asking. That layer is Run, reading Deps, which is
// the only thing in this library that sees the reader at all.
//
// Note what is NOT on Result: Deps. An adapter holds Value, Status and Input, so
// a formatter running where the adapters marshal could not obtain the zone even
// if it wanted to. The information is not merely absent from the declaration --
// it is absent from the place a declaration-driven formatter would run.
func TestAReadersTimezoneTravelsAsAValueOrNotAtAll(t *testing.T) {
	db := audienceDB(t)
	reg := services.New(resolverOver(db))
	due := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	services.MustRegister(reg, services.Spec[Deps, struct{}, zonedOut]{
		Name: "zoned", Kind: services.Query,
		Run: func(ctx services.Ctx[Deps], _ struct{}) (zonedOut, error) {
			loc, err := time.LoadLocation(probeZones[ctx.Deps.MemberID])
			if err != nil {
				return zonedOut{}, err
			}
			return zonedOut{DueAt: due, DueAtLocal: due.In(loc).Format(time.RFC3339)}, nil
		},
	})

	want := map[int64]string{
		1: `{"due_at":"2026-08-15T09:00:00Z","due_at_local":"2026-08-15T21:00:00+12:00"}`,
		2: `{"due_at":"2026-08-15T09:00:00Z","due_at_local":"2026-08-15T02:00:00-07:00"}`,
	}
	for member, expected := range want {
		res, err := reg.Dispatch(t.Context(), member, "zoned", nil)
		if err != nil {
			t.Fatalf("member %d: %v", member, err)
		}
		if got := encode(t, res.Value); got != expected {
			t.Errorf("member %d =\n  %s\nwant\n  %s", member, got, expected)
		}
	}

	// One schema, both readers. Neither is told which zone they got.
	entries := reg.Entries()
	if len(entries) != 1 {
		t.Fatalf("registered %d specs, want the probe", len(entries))
	}
	const schema = `{"type":"object","properties":` +
		`{"due_at":{"type":"string","description":"when the book must be back, RFC 3339 in UTC"},` +
		`"due_at_local":{"type":"string","description":"the same instant in the reader's own timezone"}},` +
		`"required":["due_at","due_at_local"],"additionalProperties":false}`
	if got := encode(t, entries[0].Output); got != schema {
		t.Errorf("output schema =\n  %s\nwant\n  %s", got, schema)
	}
}

// A jsonschema tag carries a description and refuses anything else.
//
// The kernel's documentation says a tag "carries a description and nothing
// else", and this is that sentence as a fact rather than a claim: the reflector
// reserves the "WORD=" prefix for future keywords and REFUSES a tag that uses
// one, so `jsonschema:"format=date-time"` is not silently swallowed -- it is a
// registration error. There is no output-side equivalent of Spec.Schema, so on
// an output field of a standard type a description is the whole vocabulary.
func TestADescriptionIsTheOnlyThingATagCanCarry(t *testing.T) {
	type tagged struct {
		DueAt time.Time `json:"due_at" jsonschema:"format=date-time"`
	}
	if _, err := jsonschema.For[tagged](nil); err == nil {
		t.Fatal("a keyword-shaped tag was accepted; the tag vocabulary has grown")
	} else if !strings.Contains(err.Error(), "must not begin with 'WORD='") {
		t.Errorf("refused for the wrong reason: %v", err)
	}

	// Spec.Schema reaches the input and only the input.
	reg := services.New(resolverOver(audienceDB(t)))
	services.MustRegister(reg, services.Spec[Deps, ListIn, ListOut]{
		Name: "hooked", Kind: services.Query,
		Schema: func(s *jsonschema.Schema) { s.Title = "reached by the hook" },
		Run:    func(services.Ctx[Deps], ListIn) (ListOut, error) { return ListOut{}, nil },
	})
	e := reg.Entries()[0]
	if !strings.Contains(encode(t, e.Input), "reached by the hook") {
		t.Error("Spec.Schema no longer reaches the input schema")
	}
	if strings.Contains(encode(t, e.Output), "reached by the hook") {
		t.Error("Spec.Schema now reaches the output schema; finding 16 needs rewriting")
	}
}

// DueDate is the answer to writing one sentence on two fields.
//
// It is EMBEDDED rather than defined -- `struct{ time.Time }` and not
// `type DueDate time.Time` -- and that is the whole trap. A defined type over a
// struct does not inherit its methods, so the defined spelling loses
// time.Time's MarshalJSON and puts `{}` on the wire while its declared schema
// still says "string". The embedded spelling promotes it and the bytes are
// unchanged.
type DueDate struct{ time.Time }

const dueDateWords = "when the book must be back, as an RFC 3339 timestamp in UTC; " +
	"convert it to the reader's own timezone before stating a date or a time"

// JSONSchema says it once, for every field of this type anywhere.
func (DueDate) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{
		Type: "string", Format: "date-time", Description: dueDateWords,
	}, nil
}

type datedOut struct {
	Borrowed DueDate `json:"borrowed_due_at"`
	Renewed  DueDate `json:"renewed_due_at"`
}

// A named type says it once, keeps the wire, and reaches a keyword a tag cannot.
//
// This is the measured answer to what annotation COSTS. The tag route is one
// sentence per field, and this module already writes the due-date sentence
// twice verbatim. The named-type route writes it once, gives both fields the
// `format: date-time` no tag can express, and changes not one byte of any
// payload -- which is what makes it a schema change rather than a wire change.
//
// What it costs in exchange is smaller than it looks, and the linter is what
// established that: embedding PROMOTES time.Time's whole method set, so
// `d.Equal(x)` and `d.Format(...)` read exactly as before and staticcheck flags
// a `.Time` written out of habit. The cost is confined to the two places the
// struct itself shows: constructing one (`DueDate{at}`) and handing the address
// of the inner value to something that wants a time.Time, such as a SQL Scan.
func TestANamedTypeSaysItOnceAndKeepsTheWire(t *testing.T) {
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	const wire = `{"borrowed_due_at":"2026-09-20T12:00:00Z","renewed_due_at":"2026-09-20T12:00:00Z"}`

	if got := encode(t, datedOut{Borrowed: DueDate{at}, Renewed: DueDate{at}}); got != wire {
		t.Errorf("wire =\n  %s\nwant\n  %s", got, wire)
	}
	// And back, because a schema change that breaks decoding is a wire change.
	var back datedOut
	if err := json.Unmarshal([]byte(wire), &back); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Called on the DueDate rather than on its inner field, because that is the
	// finding: the method set came with it.
	if !back.Borrowed.Equal(at) {
		t.Errorf("round trip gave %v, want %v", back.Borrowed, at)
	}
	// The one read that does need the field is handing a *time.Time onward.
	when := &back.Renewed.Time
	if !when.Equal(at) {
		t.Errorf("addressed inner value gave %v, want %v", when, at)
	}

	reg := services.New(resolverOver(audienceDB(t)))
	services.MustRegister(reg, services.Spec[Deps, struct{}, datedOut]{
		Name: "dated", Kind: services.Query,
		Run: func(services.Ctx[Deps], struct{}) (datedOut, error) { return datedOut{}, nil },
	})
	schema := encode(t, reg.Entries()[0].Output)
	if strings.Count(schema, dueDateWords) != 2 {
		t.Errorf("the sentence written once did not reach both fields:\n  %s", schema)
	}
	if !strings.Contains(schema, `"format":"date-time"`) {
		t.Errorf("a named type could not carry a keyword either:\n  %s", schema)
	}
}

// liarDate is the defined spelling, kept because the trap is the finding.
type liarDate time.Time

func (liarDate) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{Type: "string", Format: "date-time"}, nil
}

type liarOut struct {
	DueAt liarDate `json:"due_at"`
}

// An output may contradict its own advertised schema, and nothing notices.
//
// The kernel validates INPUT against the input schema on every dispatch. It does
// not validate output against the output schema, and neither does the MCP SDK:
// this spec advertises `{"type":"string","format":"date-time"}` and serves `{}`,
// with IsError false and no error anywhere.
//
// It matters here because SchemaFor is the ONLY channel by which an output field
// can carry anything a description cannot, so it is the channel the cost finding
// recommends -- and its failure mode is silent. Recorded rather than fixed:
// output validation is a kernel decision and this module does not make those.
func TestAnOutputMayContradictItsOwnSchemaUnnoticed(t *testing.T) {
	reg := services.New(resolverOver(audienceDB(t)))
	services.MustRegister(reg, services.Spec[Deps, struct{}, liarOut]{
		Name: "liar", Kind: services.Query,
		Run: func(services.Ctx[Deps], struct{}) (liarOut, error) {
			return liarOut{DueAt: liarDate(time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC))}, nil
		},
	})

	server := mcp.NewServer(&mcp.Implementation{Name: "liar", Version: "v0"}, nil)
	if err := mcpx.Mount(server, reg,
		func(context.Context, *mcp.CallToolRequest) (any, error) { return int64(1), nil }); err != nil {
		t.Fatalf("mount: %v", err)
	}
	clientT, serverT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), serverT, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil).
		Connect(t.Context(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encode(t, listed.Tools[0].OutputSchema), `"format":"date-time"`) {
		t.Fatalf("the probe did not advertise what it claims: %s",
			encode(t, listed.Tools[0].OutputSchema))
	}

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "liar"})
	if err != nil {
		t.Fatalf("mcp protocol error: %v", err)
	}
	if res.IsError {
		t.Fatalf("the divergence was caught after all, which would close this finding: %v",
			res.Content)
	}
	if got := encode(t, res.StructuredContent); got != `{"due_at":{}}` {
		t.Errorf("structured content = %s, want the empty object the encoder produces", got)
	}
}

// One description covers every row, so a fact that varies by row cannot be one.
//
// The fines below are the same integer and the same field, and they are not the
// same amount of money. There is one place to write what fine_cents means and
// two answers to write there -- which is why the annotation on the real Loan can
// name a currency at all, and why it says so out loud rather than pretending the
// question does not arise.
//
// A rendering tag has the identical problem for the identical reason: it is read
// once, off the type, with no row in front of it. What reaches this is a sibling
// field, which is data, and which reaches a browser too.
type pricedRow struct {
	FineCents int64  `json:"fine_cents" jsonschema:"the fine, in minor units of the branch's currency"`
	Branch    string `json:"branch"`
}

type pricedOut struct {
	Fines []pricedRow `json:"fines"`
}

func TestOneDescriptionCannotCoverRowsThatDisagree(t *testing.T) {
	reg := services.New(resolverOver(audienceDB(t)))
	services.MustRegister(reg, services.Spec[Deps, struct{}, pricedOut]{
		Name: "fines", Kind: services.Query,
		Run: func(services.Ctx[Deps], struct{}) (pricedOut, error) {
			return pricedOut{Fines: []pricedRow{
				{FineCents: 550, Branch: "London"},
				{FineCents: 550, Branch: "Tokyo"},
			}}, nil
		},
	})

	res, err := reg.Dispatch(t.Context(), int64(1), "fines", nil)
	if err != nil {
		t.Fatal(err)
	}
	const wire = `{"fines":[{"fine_cents":550,"branch":"London"},` +
		`{"fine_cents":550,"branch":"Tokyo"}]}`
	if got := encode(t, res.Value); got != wire {
		t.Fatalf("wire =\n  %s\nwant\n  %s", got, wire)
	}

	// One property, one description, both rows. There is no second place.
	schema := encode(t, reg.Entries()[0].Output)
	if strings.Count(schema, "minor units of the branch's currency") != 1 {
		t.Errorf("the description is not written exactly once:\n  %s", schema)
	}
}

// The sharpest prose a model reads here is not an output field.
//
// Finding 12 recorded this and nothing pinned it. Every agent transport serves a
// spec author's sentence verbatim, internal member id included, and no marking,
// description or formatter on an output field reaches a word of it. It is kept
// deliberately -- finding 2 settled that the service's own words are what a
// caller can act on -- and it is here so that the exposure this module actually
// has is measured rather than assumed.
func TestARefusalReachesEveryAgentTransportVerbatim(t *testing.T) {
	const said = "permission denied: member 2 is suspended"

	server := mcp.NewServer(&mcp.Implementation{Name: "l", Version: "v0"}, nil)
	if err := mcpx.Mount(server, registryAt(audienceDB(t)),
		func(context.Context, *mcp.CallToolRequest) (any, error) { return int64(2), nil }); err != nil {
		t.Fatalf("mcpx mount: %v", err)
	}
	clientT, serverT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), serverT, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil).
		Connect(t.Context(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.CallTool(t.Context(),
		&mcp.CallToolParams{Name: "borrow_book", Arguments: map[string]any{"book_id": 10}})
	if err != nil {
		t.Fatalf("mcp protocol error: %v", err)
	}
	var mcpText strings.Builder
	for _, block := range res.Content {
		if content, ok := block.(*mcp.TextContent); ok {
			mcpText.WriteString(content.Text)
		}
	}
	if !res.IsError || mcpText.String() != said {
		t.Errorf("mcpx served isError=%v %q, want an error carrying %q",
			res.IsError, mcpText.String(), said)
	}

	ts, err := adkx.Toolset(registryAt(audienceDB(t)), func(ctx agent.Context) (any, error) {
		return strconv.ParseInt(ctx.UserID(), 10, 64)
	})
	if err != nil {
		t.Fatalf("adkx toolset: %v", err)
	}
	actx := &adkContext{StrictContextMock: agent.NewStrictContextMock(t.Context()), member: 2}
	tools, err := ts.Tools(actx)
	if err != nil {
		t.Fatalf("adkx tools: %v", err)
	}
	var ran bool
	for _, one := range tools {
		if one.Name() != "borrow_book" {
			continue
		}
		ran = true
		if _, err := one.(adkx.RunnableTool).Run(actx, map[string]any{"book_id": 10}); err == nil {
			t.Error("adkx allowed a suspended member to borrow")
		} else if err.Error() != said {
			t.Errorf("adkx served %q, want %q", err, said)
		}
	}
	if !ran {
		t.Fatal("adkx published no borrow_book")
	}

	toolbox, err := aguix.NewToolbox(registryAt(audienceDB(t)), func(context.Context) (any, error) {
		return int64(2), nil
	})
	if err != nil {
		t.Fatalf("NewToolbox: %v", err)
	}
	handler, err := aguix.Handler(Librarian(toolbox))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(
		`{"threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":"borrow book 10"}]}`)))
	var content string
	for _, block := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n") {
		var event map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(block, "data: ")), &event) != nil {
			continue
		}
		if event["type"] == "TOOL_CALL_RESULT" {
			content = fmt.Sprint(event["content"])
		}
	}
	if content != "Error: "+said {
		t.Errorf("aguix served %q, want the refusal behind its error prefix", content)
	}
}
