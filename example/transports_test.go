package example

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/v2/agent"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"github.com/Artui/go-services/ginx"
	"github.com/Artui/go-services/httpx"
	"github.com/Artui/go-services/mcpx"
)

// memberHeader is how the two HTTP adapters learn who is calling. A header
// rather than a field on the input, because the whole point of identity living
// in Deps is that a caller cannot name the member.
const memberHeader = "X-Member-Id"

func headerPrincipal(r *http.Request) (any, error) {
	id, err := strconv.ParseInt(r.Header.Get(memberHeader), 10, 64)
	if err != nil {
		// Nil rather than an error: "nobody is authenticated" is the resolver's
		// decision to refuse, and an adapter that turns a missing header into a
		// transport error takes that decision away from the application.
		return nil, nil
	}
	return id, nil
}

// transport is one mounted adapter, reduced to what this file compares.
type transport struct {
	name   string
	borrow func(t *testing.T, member, book int64) (failed bool)
}

// The route table, shared in spirit by the two HTTP adapters and written twice
// because their capture syntax genuinely differs: net/http writes {book_id}
// and Gin writes :book_id. That is the one thing the kernel does not unify,
// and mounting the same registry on both is how you find out it is the only
// one.
func mountHTTPX(t *testing.T, db *sql.DB) transport {
	t.Helper()
	mux := http.NewServeMux()
	err := httpx.Mount(mux, Registry(db), map[string]httpx.Route{
		"borrow_book": {
			Method: "POST", Pattern: "/books/{book_id}/loans",
			Location: "/loans/{loan_id}",
		},
		"list_books": {Method: "GET", Pattern: "/books"},
	}, headerPrincipal)
	if err != nil {
		t.Fatalf("httpx mount: %v", err)
	}

	return transport{name: "httpx", borrow: func(t *testing.T, member, book int64) bool {
		t.Helper()
		req := httptest.NewRequest("POST", fmt.Sprintf("/books/%d/loans", book), nil)
		req.Header.Set(memberHeader, strconv.FormatInt(member, 10))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code >= 400
	}}
}

func mountGinx(t *testing.T, db *sql.DB) transport {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	err := ginx.Mount(engine, Registry(db), map[string]ginx.Route{
		"borrow_book": {
			Method: "POST", Path: "/books/:book_id/loans",
			Location: "/loans/{loan_id}",
		},
		"list_books": {Method: "GET", Path: "/books"},
	}, func(c *gin.Context) (any, error) { return headerPrincipal(c.Request) })
	if err != nil {
		t.Fatalf("ginx mount: %v", err)
	}

	return transport{name: "ginx", borrow: func(t *testing.T, member, book int64) bool {
		t.Helper()
		req := httptest.NewRequest("POST", fmt.Sprintf("/books/%d/loans", book), nil)
		req.Header.Set(memberHeader, strconv.FormatInt(member, 10))
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec.Code >= 400
	}}
}

// The MCP session has no headers, so the member is carried by the mount. That
// is not a workaround: an MCP server is typically already connected as one
// principal, and the adapter's Principal signature takes a context and the
// request for exactly this reason.
func mountMCPX(t *testing.T, db *sql.DB) transport {
	t.Helper()
	member := new(int64)

	server := mcp.NewServer(&mcp.Implementation{Name: "library", Version: "v0"}, nil)
	err := mcpx.Mount(server, Registry(db),
		func(context.Context, *mcp.CallToolRequest) (any, error) { return *member, nil })
	if err != nil {
		t.Fatalf("mcpx mount: %v", err)
	}

	clientT, serverT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), serverT, nil); err != nil {
		t.Fatalf("mcp server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "library-client", Version: "v0"}, nil)
	session, err := client.Connect(t.Context(), clientT, nil)
	if err != nil {
		t.Fatalf("mcp client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return transport{name: "mcpx", borrow: func(t *testing.T, m, book int64) bool {
		t.Helper()
		*member = m
		res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
			Name: "borrow_book", Arguments: map[string]any{"book_id": book},
		})
		if err != nil {
			t.Fatalf("mcp protocol error, want a tool result: %v", err)
		}
		return res.IsError
	}}
}

// Every adapter this project is mounted on. A transport added here is
// immediately held to the same three assertions below, which is the point of
// keeping the example rather than deleting it.
var mounts = []func(*testing.T, *sql.DB) transport{
	mountHTTPX, mountGinx, mountMCPX, mountADKX,
}

// dumpState is the comparison. Reading the tables back is the only way to tell
// three transports apart on the thing that matters -- a status code says what
// the adapter decided, not what the database did.
func dumpState(t *testing.T, db *sql.DB) string {
	t.Helper()
	var b strings.Builder

	rows, err := db.QueryContext(t.Context(),
		`SELECT id, available FROM books ORDER BY id`)
	if err != nil {
		t.Fatalf("books: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, available int64
		if err := rows.Scan(&id, &available); err != nil {
			t.Fatalf("scan book: %v", err)
		}
		fmt.Fprintf(&b, "book %d available %d\n", id, available)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("books: %v", err)
	}

	loans, err := db.QueryContext(t.Context(),
		`SELECT book_id, member_id FROM loans ORDER BY id`)
	if err != nil {
		t.Fatalf("loans: %v", err)
	}
	defer loans.Close()
	for loans.Next() {
		var book, member int64
		if err := loans.Scan(&book, &member); err != nil {
			t.Fatalf("scan loan: %v", err)
		}
		fmt.Fprintf(&b, "loan book %d member %d\n", book, member)
	}
	if err := loans.Err(); err != nil {
		t.Fatalf("loans: %v", err)
	}
	return b.String()
}

// forEachTransport runs one borrow on a fresh database per adapter and returns
// what each left behind, keyed by adapter name.
func forEachTransport(t *testing.T, member, book int64) (map[string]string, map[string]bool) {
	t.Helper()
	states := map[string]string{}
	failures := map[string]bool{}

	for _, mount := range mounts {
		db := newDB(t)
		tr := mount(t, db)
		failures[tr.name] = tr.borrow(t, member, book)
		states[tr.name] = dumpState(t, db)
	}
	return states, failures
}

// agree asserts every transport produced the same answer, naming the two that
// differ rather than reporting a count.
func agree[T comparable](t *testing.T, what string, got map[string]T) T {
	t.Helper()
	var first T
	var firstName string
	for _, name := range []string{"httpx", "ginx", "mcpx", "adkx"} {
		v, ok := got[name]
		if !ok {
			t.Fatalf("%s: no result for %s", what, name)
		}
		if firstName == "" {
			first, firstName = v, name
			continue
		}
		if v != first {
			t.Errorf("%s: %s produced %v, %s produced %v", what, firstName, first, name, v)
		}
	}
	return first
}

// A successful borrow leaves the same database behind whichever transport
// carried it. This is the thesis, stated as a fact about rows rather than about
// response bodies.
func TestEveryTransportLeavesTheSameState(t *testing.T) {
	states, failures := forEachTransport(t, 1, 10)

	if failed := agree(t, "failure", failures); failed {
		t.Fatalf("every transport failed a borrow that should have succeeded")
	}
	state := agree(t, "state", states)

	want := "book 10 available 1\nbook 11 available 0\nloan book 10 member 1\n"
	if state != want {
		t.Errorf("state =\n%s\nwant\n%s", state, want)
	}
}

// And a rolled-back borrow leaves the same nothing behind. A transport that
// committed before noticing the refusal would show an orphan loan here, on that
// transport only.
func TestEveryTransportRollsBackTheSameWay(t *testing.T) {
	states, failures := forEachTransport(t, 1, 11)

	if failed := agree(t, "failure", failures); !failed {
		t.Fatalf("every transport allowed a borrow of a book with no copies")
	}
	state := agree(t, "state", states)

	want := "book 10 available 2\nbook 11 available 0\n"
	if state != want {
		t.Errorf("state =\n%s\nwant\n%s", state, want)
	}
}

// Permission is a property of the registry, not of the adapter, so all three
// refuse the suspended member and none of them writes anything.
func TestEveryTransportRefusesASuspendedMember(t *testing.T) {
	states, failures := forEachTransport(t, 2, 10)

	if failed := agree(t, "failure", failures); !failed {
		t.Fatalf("every transport let a suspended member borrow")
	}
	state := agree(t, "state", states)

	want := "book 10 available 2\nbook 11 available 0\n"
	if state != want {
		t.Errorf("state =\n%s\nwant\n%s", state, want)
	}
}

// The route tables above are the adapters' only per-transport configuration,
// and a capture naming a field the operation does not declare is refused at
// mount rather than at request time. Asserting it here keeps the example
// honest about what "declare once" does and does not cover.
func TestAMisspeltCaptureIsRefusedAtMount(t *testing.T) {
	db := newDB(t)
	err := httpx.Mount(http.NewServeMux(), Registry(db), map[string]httpx.Route{
		"borrow_book": {Method: "POST", Pattern: "/books/{bookID}/loans"},
	}, headerPrincipal)

	if err == nil {
		t.Fatal("mount accepted a capture the input declares no field for")
	}
	if !strings.Contains(err.Error(), "bookID") {
		t.Errorf("error does not name the capture: %v", err)
	}
	// Configuration, not validation: no request the caller could send would
	// fix it, so it must not reach a client as a 400.
	if !errors.Is(err, services.ErrConfiguration) {
		t.Errorf("err = %v, want ErrConfiguration", err)
	}
}

// adkTool is the shape ADK dispatches on. adkx names it, so this module does
// not have to restate an interface the compiler would not tie to anything.
type adkTool = adkx.RunnableTool

// adkContext is an ADK invocation for one member.
//
// StrictContextMock is ADK's own double and implements the whole interface, so
// this keeps compiling as agent.Context grows -- which matters here because
// none of that surface is ours.
type adkContext struct {
	agent.StrictContextMock
	member int64
}

// UserID is where the principal comes from. ADK has no headers, so identity
// rides on the session rather than on the call, and adkx.UserID reads exactly
// this -- but a member is a number here and ADK's is a string, which is the one
// piece of glue an application has to write.
func (c *adkContext) UserID() string { return strconv.FormatInt(c.member, 10) }

func mountADKX(t *testing.T, db *sql.DB) transport {
	t.Helper()
	ts, err := adkx.Toolset(Registry(db), func(ctx agent.Context) (any, error) {
		id, err := strconv.ParseInt(ctx.UserID(), 10, 64)
		if err != nil {
			// Nil rather than an error, for the same reason the HTTP principal
			// does it: "nobody is authenticated" is the resolver's decision to
			// refuse, and an adapter that made it here would take that decision
			// away from the application.
			return nil, nil
		}
		return id, nil
	})
	if err != nil {
		t.Fatalf("adkx toolset: %v", err)
	}

	ctx := &adkContext{StrictContextMock: agent.NewStrictContextMock(t.Context())}
	published, err := ts.Tools(ctx)
	if err != nil {
		t.Fatalf("adkx tools: %v", err)
	}

	var borrow adkTool
	for _, one := range published {
		if one.Name() != "borrow_book" {
			continue
		}
		runnable, ok := one.(adkTool)
		if !ok {
			t.Fatalf("adkx published borrow_book in a shape ADK cannot run: %T", one)
		}
		borrow = runnable
	}
	if borrow == nil {
		t.Fatal("adkx published no borrow_book")
	}

	return transport{name: "adkx", borrow: func(t *testing.T, m, book int64) bool {
		t.Helper()
		ctx.member = m
		_, err := borrow.Run(ctx, map[string]any{"book_id": book})
		return err != nil
	}}
}
