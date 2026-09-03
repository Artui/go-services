package mcpx_test

// The fixtures every test in this package shares: one registry that exercises
// each declaration a tool definition reads and each error the taxonomy names,
// and a helper that puts a real MCP client on the other end of a real MCP
// server. Nothing here asserts; assertions live with the behaviour they cover.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Artui/go-services"
	"github.com/Artui/go-services/mcpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// deps is the per-call dependency value. It carries the acting principal, which
// is how a test proves the value a Principal returned actually reached a
// service rather than being dropped somewhere in the mount.
type deps struct {
	actor string
}

// resolve is the registry's identity assertion: the adapter hands over an
// opaque principal, and this is where it becomes typed.
func resolve(_ context.Context, principal any) (deps, error) {
	if principal == nil {
		return deps{actor: "anonymous"}, nil
	}
	actor, ok := principal.(string)
	if !ok {
		return deps{}, fmt.Errorf("unexpected principal %T", principal)
	}
	return deps{actor: actor}, nil
}

type authorIn struct {
	ID int `json:"id" jsonschema:"the author's identifier"`
}

type author struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	SeenBy string `json:"seen_by"`
}

type createIn struct {
	Name  string   `json:"name"`
	Tags  []string `json:"tags,omitempty"`
	Draft bool     `json:"draft,omitempty"`
}

// wideIn exists for the integer-fidelity test: a value above 2^53 survives JSON
// text and does not survive a trip through float64.
type wideIn struct {
	N int64 `json:"n"`
}

// twoFieldIn reports two field errors at once, so a rendering test can check
// that both reach the model and that the order is stable.
type twoFieldIn struct {
	Email string `json:"email"`
	Age   int    `json:"age,omitempty"`
}

func (v twoFieldIn) Validate() error {
	return &services.ValidationError{Fields: map[string][]string{
		"email":                {"must contain an at sign", "must not be a role address"},
		"age":                  {"must be at least 18"},
		services.NonFieldKey:   {"an author needs either an email or a phone number"},
		"zzz_sorts_after_them": {"and the list is sorted"},
	}}
}

// blankIn raises a ValidationError that attributes nothing, which is the shape
// a rendering test needs and which no realistic Validate method produces.
type blankIn struct {
	Anything string `json:"anything,omitempty"`
}

func (v blankIn) Validate() error { return &services.ValidationError{} }

type empty struct{}

// truth returns a pointer to b, for the *bool declarations.
func truth(b bool) *bool { return &b }

// newRegistry builds the fixture registry. Registration order is the order
// tools are advertised in, and one test depends on that.
func newRegistry(t *testing.T) *services.Registry[deps] {
	t.Helper()
	reg := services.New(resolve)

	// A query with idempotency undeclared: the common shape, and the one whose
	// annotations are the interesting case.
	must(t, services.Register(reg, services.Spec[deps, authorIn, author]{
		Name:        "authors.get",
		Description: "Fetch one author by identifier.",
		Kind:        services.Query,
		Run: func(c services.Ctx[deps], in authorIn) (author, error) {
			return author{ID: in.ID, Name: "Ursula", SeenBy: c.Deps.actor}, nil
		},
	}))

	// A mutation with idempotency undeclared: the case that must publish no
	// annotations at all rather than an invented "idempotentHint": false.
	must(t, services.Register(reg, services.Spec[deps, createIn, author]{
		Name:        "authors.create",
		Description: "Create an author.",
		Kind:        services.Mutation,
		Status:      201,
		Run: func(c services.Ctx[deps], in createIn) (author, error) {
			return author{ID: 1, Name: in.Name, SeenBy: c.Deps.actor}, nil
		},
	}))

	// A mutation declaring itself idempotent: the only combination the wire can
	// carry unambiguously.
	must(t, services.Register(reg, services.Spec[deps, authorIn, author]{
		Name:       "authors.replace",
		Kind:       services.Mutation,
		Idempotent: truth(true),
		Run: func(_ services.Ctx[deps], in authorIn) (author, error) {
			return author{ID: in.ID, Name: "replaced"}, nil
		},
	}))

	// A create declaring itself additive. This is the case Destructive exists
	// for: without it, MCP's destructiveHint defaults to true for any
	// non-read-only tool, so an approval gate prompts on every create.
	// Idempotency is left undeclared, which makes this also the case where the
	// block has to be attached for one hint while the other rides along.
	must(t, services.Register(reg, services.Spec[deps, createIn, author]{
		Name:        "authors.draft",
		Kind:        services.Mutation,
		Destructive: truth(false),
		Run: func(_ services.Ctx[deps], in createIn) (author, error) {
			return author{ID: 2, Name: in.Name}, nil
		},
	}))

	// A mutation declaring both, and the only spec here that declares
	// Idempotent false. That declaration is invisible on the wire -- it encodes
	// identically to authors.create's silence, because the SDK's
	// IdempotentHint is a plain bool: the protocol carries two states where the
	// kernel declares three. The destructiveHint beside it does survive.
	must(t, services.Register(reg, services.Spec[deps, authorIn, empty]{
		Name:        "authors.delete",
		Kind:        services.Mutation,
		Idempotent:  truth(false),
		Destructive: truth(true),
		Run:         func(services.Ctx[deps], authorIn) (empty, error) { return empty{}, nil },
	}))

	// A query that also declares idempotency, so the read-only branch is
	// covered with the hint both present and absent.
	must(t, services.Register(reg, services.Spec[deps, empty, int]{
		Name:       "authors.count",
		Kind:       services.Query,
		Idempotent: truth(true),
		Run:        func(services.Ctx[deps], empty) (int, error) { return 2, nil },
	}))

	must(t, services.Register(reg, services.Spec[deps, wideIn, wideIn]{
		Name: "echo.wide",
		Kind: services.Query,
		Run:  func(_ services.Ctx[deps], in wideIn) (wideIn, error) { return in, nil },
	}))

	must(t, services.Register(reg, services.Spec[deps, twoFieldIn, empty]{
		Name: "fail.validate",
		Kind: services.Query,
		Run:  func(services.Ctx[deps], twoFieldIn) (empty, error) { return empty{}, nil },
	}))

	must(t, services.Register(reg, services.Spec[deps, blankIn, empty]{
		Name: "fail.blank",
		Kind: services.Query,
		Run:  func(services.Ctx[deps], blankIn) (empty, error) { return empty{}, nil },
	}))

	// The three taxonomy sentinels, each wrapped the way a service would wrap
	// them, so the test proves errors.Is survives the consumer's own words.
	must(t, services.Register(reg, services.Spec[deps, authorIn, empty]{
		Name: "fail.permission",
		Kind: services.Query,
		Run: func(_ services.Ctx[deps], in authorIn) (empty, error) {
			return empty{}, fmt.Errorf("%w: author %d belongs to someone else", services.ErrPermission, in.ID)
		},
	}))

	must(t, services.Register(reg, services.Spec[deps, authorIn, empty]{
		Name: "fail.notfound",
		Kind: services.Query,
		Run: func(_ services.Ctx[deps], in authorIn) (empty, error) {
			return empty{}, fmt.Errorf("%w: no author %d", services.ErrNotFound, in.ID)
		},
	}))

	must(t, services.Register(reg, services.Spec[deps, authorIn, empty]{
		Name: "fail.conflict",
		Kind: services.Mutation,
		Run: func(_ services.Ctx[deps], in authorIn) (empty, error) {
			return empty{}, fmt.Errorf("%w: author %d still has books", services.ErrConflict, in.ID)
		},
	}))

	// The failure outside the taxonomy, whose words must not reach the client.
	must(t, services.Register(reg, services.Spec[deps, empty, empty]{
		Name: "fail.internal",
		Kind: services.Query,
		Run: func(services.Ctx[deps], empty) (empty, error) {
			return empty{}, errors.New("dial tcp 10.0.0.7:5432: connection refused")
		},
	}))

	// A service whose return value cannot be encoded. The declared Out is a map
	// of anything, so the schema reflects, and the value in it is a func, so the
	// encoder refuses at call time rather than at registration.
	must(t, services.Register(reg, services.Spec[deps, empty, map[string]any]{
		Name: "fail.encode",
		Kind: services.Query,
		Run: func(services.Ctx[deps], empty) (map[string]any, error) {
			return map[string]any{"callback": func() {}}, nil
		},
	}))

	// A permission gate, to prove Permit refusals travel the same path as a
	// refusal returned from Run.
	must(t, services.Register(reg, services.Spec[deps, empty, empty]{
		Name: "fail.permit",
		Kind: services.Query,
		Permit: []func(services.Ctx[deps], empty) error{
			func(c services.Ctx[deps], _ empty) error {
				return fmt.Errorf("%w: %s may not do this", services.ErrPermission, c.Deps.actor)
			},
		},
		Run: func(services.Ctx[deps], empty) (empty, error) { return empty{}, nil },
	}))

	return reg
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("registering the fixture registry: %v", err)
	}
}

// connect mounts reg on a fresh server and returns a client session talking to
// it over the SDK's in-memory transport.
//
// The whole point of running the real pair is that everything asserted
// downstream is what a client received after a JSON round trip, rather than
// what the mount handed the server in Go.
func connect(t *testing.T, reg *services.Registry[deps], principal mcpx.Principal, opts ...mcpx.Option) *mcp.ClientSession {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "v1"}, nil)
	if err := mcpx.Mount(srv, reg, principal, opts...); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return dial(t, srv)
}

// dial connects a client to an already-populated server.
func dial(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()

	ctx := t.Context()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting the server: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connecting the client: %v", err)
	}
	t.Cleanup(func() {
		clientSession.Close()
		serverSession.Wait()
	})
	return clientSession
}

// call runs one tool and fails the test if the protocol itself failed. A tool
// that refused is not a protocol failure, so the result is returned either way
// and the caller asserts on IsError.
func call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%q): %v", name, err)
	}
	return res
}

// text returns the single text block a result carries.
func text(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("expected exactly one content block, got %d", len(res.Content))
	}
	block, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected a text block, got %T", res.Content[0])
	}
	return block.Text
}

// tools lists the server's tools, keyed by name.
func tools(t *testing.T, cs *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	byName := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}
	return byName
}

// wiretap records the raw JSON-RPC frames a client reads.
//
// It exists because the Go client's own view of a result is not the wire: its
// decoder turns every JSON number into a float64, so a client-side assertion
// cannot tell a value the server mangled from one the client's own decoder
// rounded. Asserting on the recorded bytes settles which of the two happened.
type wiretap struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *wiretap) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// lastResult returns the "result" object of the most recent JSON-RPC response
// the client read.
func (w *wiretap) lastResult(t *testing.T) json.RawMessage {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()

	var last json.RawMessage
	for line := range strings.SplitSeq(w.buf.String(), "\n") {
		frame, ok := strings.CutPrefix(line, "read: ")
		if !ok {
			continue
		}
		var response struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(frame), &response); err != nil {
			t.Fatalf("decoding a recorded frame: %v", err)
		}
		if len(response.Result) > 0 {
			last = response.Result
		}
	}
	if last == nil {
		t.Fatal("no JSON-RPC result was recorded")
	}
	return last
}

// tapped mounts reg and returns a client session plus the recorder holding
// everything that session read.
func tapped(t *testing.T, reg *services.Registry[deps]) (*mcp.ClientSession, *wiretap) {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "v1"}, nil)
	if err := mcpx.Mount(srv, reg, nil); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	ctx := t.Context()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connecting the server: %v", err)
	}
	tap := &wiretap{}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v1"}, nil)
	clientSession, err := client.Connect(ctx, &mcp.LoggingTransport{Transport: clientTransport, Writer: tap}, nil)
	if err != nil {
		t.Fatalf("connecting the client: %v", err)
	}
	t.Cleanup(func() {
		clientSession.Close()
		serverSession.Wait()
	})
	return clientSession, tap
}
