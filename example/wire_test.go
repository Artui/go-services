package example

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Artui/go-services/ginx"
	"github.com/Artui/go-services/httpx"
	"github.com/Artui/go-services/mcpx"
)

// The bodies below are written out in full on purpose, and this is the only
// place in the repository that does it.
//
// The adapters' own suites build their expectations from the sentinel, so they
// assert the composition -- the sentinel's words, then the service's -- and
// would keep passing if a sentinel's wording became nonsense. The kernel's
// TestClientFacingSentinelsCarryNoPackagePrefix asserts the property. Neither
// of them reads what a client actually receives, and that is the thing the
// ergonomics pass changed.
//
// So this test spells the answer out, end to end, through a real registry over
// a real database. If the wire is ever wrong again, something here fails with
// the wrong string printed next to the right one.
func TestTheWireCarriesNoPackageName(t *testing.T) {
	cases := []struct {
		name         string
		member, book int64
		status       int
		body         string
		mcpText      string
	}{
		{
			name:   "a conflict keeps the service's words and names no package",
			member: 1, book: 11, status: http.StatusConflict,
			body:    `{"error":"conflict: no copy of \"Structure and Interpretation\" is on the shelf"}`,
			mcpText: `conflict: no copy of "Structure and Interpretation" is on the shelf`,
		},
		{
			name:   "a permission refusal keeps the Permit function's words",
			member: 2, book: 10, status: http.StatusForbidden,
			body:    `{"error":"permission denied: member 2 is suspended"}`,
			mcpText: `permission denied: member 2 is suspended`,
		},
		{
			name:   "a missing row keeps the service's words",
			member: 1, book: 999, status: http.StatusNotFound,
			body:    `{"error":"not found: no book 999"}`,
			mcpText: `not found: no book 999`,
		},
		{
			// The other shape, and the reason a client has to branch on the
			// status: per-field messages under "errors", never "error".
			name:   "a validation failure answers the other shape",
			member: 1, book: 0, status: http.StatusBadRequest,
			body:    `{"errors":{"book_id":["must be a positive identifier"]}}`,
			mcpText: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, adapter := range []string{"httpx", "ginx"} {
				status, body := serveBorrow(t, adapter, tc.member, tc.book)
				if status != tc.status {
					t.Errorf("%s: status = %d, want %d", adapter, status, tc.status)
				}
				if body != tc.body {
					t.Errorf("%s: body =\n  %s\nwant\n  %s", adapter, body, tc.body)
				}
			}
			if tc.mcpText == "" {
				return
			}
			if got := callBorrow(t, tc.member, tc.book); got != tc.mcpText {
				t.Errorf("mcpx: text =\n  %s\nwant\n  %s", got, tc.mcpText)
			}
		})
	}
}

// serveBorrow drives one borrow over whichever HTTP adapter is named, against a
// database of its own.
func serveBorrowFull(t *testing.T, adapter string, member, book int64) (int, string, string) {
	t.Helper()
	db := newDB(t)

	var handler http.Handler
	switch adapter {
	case "httpx":
		mux := http.NewServeMux()
		if err := httpx.Mount(mux, Registry(db), map[string]httpx.Route{
			"borrow_book": {
				Method: "POST", Pattern: "/books/{book_id}/loans",
				Location: "/loans/{loan_id}",
			},
		}, headerPrincipal); err != nil {
			t.Fatalf("httpx mount: %v", err)
		}
		handler = mux
	case "ginx":
		gin.SetMode(gin.TestMode)
		engine := gin.New()
		if err := ginx.Mount(engine, Registry(db), map[string]ginx.Route{
			"borrow_book": {
				Method: "POST", Path: "/books/:book_id/loans",
				Location: "/loans/{loan_id}",
			},
		}, func(c *gin.Context) (any, error) { return headerPrincipal(c.Request) }); err != nil {
			t.Fatalf("ginx mount: %v", err)
		}
		handler = engine
	default:
		t.Fatalf("no adapter named %q", adapter)
	}

	req := httptest.NewRequest("POST", fmt.Sprintf("/books/%d/loans", book), nil)
	req.Header.Set(memberHeader, strconv.FormatInt(member, 10))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, strings.TrimSpace(rec.Body.String()), rec.Header().Get("Location")
}

// serveBorrow is the status-and-body half, for the tests that do not care where
// the loan ended up.
func serveBorrow(t *testing.T, adapter string, member, book int64) (int, string) {
	t.Helper()
	status, body, _ := serveBorrowFull(t, adapter, member, book)
	return status, body
}

// callBorrow drives the same operation over MCP, where there is no status and
// the words are the whole answer.
func callBorrow(t *testing.T, member, book int64) string {
	t.Helper()
	db := newDB(t)

	server := mcp.NewServer(&mcp.Implementation{Name: "library", Version: "v0"}, nil)
	if err := mcpx.Mount(server, Registry(db),
		func(context.Context, *mcp.CallToolRequest) (any, error) { return member, nil }); err != nil {
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

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "borrow_book", Arguments: map[string]any{"book_id": book},
	})
	if err != nil {
		t.Fatalf("mcp protocol error, want a tool result: %v", err)
	}
	if !res.IsError {
		t.Fatal("mcpx did not report a refusal")
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return text.String()
}

// A 201 that says where the thing it created lives.
//
// This is the finding FRICTION.md recorded and the ergonomics pass deferred:
// Result carries no channel for a header, so the template lives on the route,
// which is the only thing in the system that knows the URL shape. Both HTTP
// adapters build it from the kernel, so both answer the same path.
func TestACreatedLoanSaysWhereItLives(t *testing.T) {
	for _, adapter := range []string{"httpx", "ginx"} {
		status, body, location := serveBorrowFull(t, adapter, 1, 10)
		if status != http.StatusCreated {
			t.Fatalf("%s: status = %d, want 201 (body %s)", adapter, status, body)
		}
		// Loan 3, because each adapter runs against a database of its own and
		// the seed has already written two.
		if location != "/loans/3" {
			t.Errorf("%s: Location = %q, want %q", adapter, location, "/loans/3")
		}
	}
}

// A refusal points nowhere, because nothing was created.
func TestARefusedBorrowSaysNothingAboutWhere(t *testing.T) {
	for _, adapter := range []string{"httpx", "ginx"} {
		status, _, location := serveBorrowFull(t, adapter, 1, 11)
		if status != http.StatusConflict {
			t.Fatalf("%s: status = %d, want 409", adapter, status)
		}
		if location != "" {
			t.Errorf("%s: Location = %q on a 409", adapter, location)
		}
	}
}
