package conformance_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/Artui/go-services/conformance"
)

// A case is one operation exercised the same way on every transport. The HTTP
// target and the MCP arguments are two spellings of one request; where they
// cannot be (a malformed body has no map form) the MCP leg is skipped and the
// case says so.
type conformanceCase struct {
	name   string
	spec   string
	method string
	target string // std pattern; the Gin path is derived from the table
	ginURL string
	body   string
	args   map[string]any
	// skipMapped is a reason, when the case cannot be expressed as arguments.
	// It covers MCP and ADK together: both take a map, so a malformed body has
	// no form either of them can be given.
	skipMapped string
}

func cases() []conformanceCase {
	return []conformanceCase{
		{
			name: "success", spec: "create_author", method: "POST",
			target: "/authors", ginURL: "/authors",
			body: `{"name":"ada","bio":"first"}`,
			args: map[string]any{"name": "ada", "bio": "first"},
		},
		{
			// The precision case. A map[string]any round trip rewrites this.
			name: "an identifier past 2^53 survives", spec: "create_author", method: "POST",
			target: "/authors", ginURL: "/authors",
			body: `{"name":"ada","id":9007199254740993}`,
			args: map[string]any{"name": "ada", "id": int64(9007199254740993)},
		},
		{
			name: "schema failure: missing required", spec: "create_author", method: "POST",
			target: "/authors", ginURL: "/authors",
			body: `{}`, args: map[string]any{},
		},
		{
			name: "schema failure: wrong type", spec: "create_author", method: "POST",
			target: "/authors", ginURL: "/authors",
			body: `{"name":123}`, args: map[string]any{"name": 123},
		},
		{
			name: "schema failure: unknown field", spec: "create_author", method: "POST",
			target: "/authors", ginURL: "/authors",
			body: `{"name":"ada","surprise":1}`,
			args: map[string]any{"name": "ada", "surprise": 1},
		},
		{
			name: "business rule", spec: "create_author", method: "POST",
			target: "/authors", ginURL: "/authors",
			body: `{"name":"   "}`, args: map[string]any{"name": "   "},
		},
		{
			name: "malformed body", spec: "create_author", method: "POST",
			target: "/authors", ginURL: "/authors",
			body: `{`, skipMapped: "arguments are a map; there is no malformed form of one",
		},
		{
			name: "a valid non-object body", spec: "create_author", method: "POST",
			target: "/authors", ginURL: "/authors",
			body: `[1,2]`, skipMapped: "arguments are a map; an array cannot be sent",
		},
		{
			name: "no arguments at all", spec: "no_args", method: "GET",
			target: "/ping", ginURL: "/ping", args: map[string]any{},
		},
		{
			name: "a route capture reaches the operation", spec: "get_author", method: "GET",
			target: "/authors/ada", ginURL: "/authors/ada",
			args: map[string]any{"name": "ada"},
		},
		{
			name: "omitted is not the same as zero", spec: "patch_author", method: "PATCH",
			target: "/authors", ginURL: "/authors",
			body: `{"bio":null}`, args: map[string]any{"bio": nil},
		},
		{
			name: "permission", spec: "denied", method: "POST",
			target: "/denied", ginURL: "/denied",
			body: `{"name":"ada"}`, args: map[string]any{"name": "ada"},
		},
		{
			name: "not found", spec: "missing", method: "POST",
			target: "/missing", ginURL: "/missing",
			body: `{"name":"ada"}`, args: map[string]any{"name": "ada"},
		},
		{
			name: "conflict", spec: "clash", method: "POST",
			target: "/clash", ginURL: "/clash",
			body: `{"name":"ada"}`, args: map[string]any{"name": "ada"},
		},
		{
			// The redaction case. An operator's words must not reach a client.
			name: "an unexpected error", spec: "boom", method: "POST",
			target: "/boom", ginURL: "/boom",
			body: `{"name":"ada"}`, args: map[string]any{"name": "ada"},
		},
		{
			// The process-killer. errors.As matches a typed nil, so a renderer
			// without a nil check dereferences it.
			name: "a typed-nil validation error", spec: "typed_nil", method: "POST",
			target: "/typed_nil", ginURL: "/typed_nil",
			body: `{"name":"ada"}`, args: map[string]any{"name": "ada"},
		},
		{
			// The divergence a per-adapter suite cannot see: one adapter
			// committed its status before encoding and answered 200 with an
			// empty body where the other answered 500.
			name: "a success value that cannot be encoded", spec: "unencodable", method: "GET",
			target: "/unencodable?name=ada", ginURL: "/unencodable?name=ada",
			args: map[string]any{"name": "ada"},
		},
	}
}

// TestEveryTransportAgrees is the executable form of the library's claim. One
// registry, three adapters, and a difference between any two of them fails
// here rather than reaching a client.
func TestEveryTransportAgrees(t *testing.T) {
	reg := conformance.Registry()
	mux := mountHTTPX(t, reg)
	engine := mountGinx(t, reg)
	session := mountMCPX(t, reg)
	adkTools := mountADKX(t, reg)

	for _, tc := range cases() {
		t.Run(tc.name, func(t *testing.T) {
			std := serveHTTP(mux, tc.method, tc.target, tc.body)
			gin := serveHTTP(engine, tc.method, tc.ginURL, tc.body)

			// The two HTTP adapters share a wire format, so anything less than
			// byte equality between them is a difference a client can observe.
			if std.Status != gin.Status {
				t.Errorf("HTTP status diverges: httpx=%d ginx=%d", std.Status, gin.Status)
			}
			if std.Location != gin.Location {
				t.Errorf("Location diverges: httpx=%q ginx=%q", std.Location, gin.Location)
			}
			if std.Wire != gin.Wire {
				t.Errorf("HTTP body diverges:\n  httpx: %s\n  ginx:  %s", std.Wire, gin.Wire)
			}

			// No transport may put an operator's words on a client's wire.
			for name, out := range map[string]conformance.Outcome{"httpx": std, "ginx": gin} {
				if out.Leaked {
					t.Errorf("%s leaked the internal error text: %s", name, out.Wire)
				}
			}

			if tc.skipMapped != "" {
				t.Logf("MCP and ADK legs skipped: %s", tc.skipMapped)
				return
			}

			// The two transports that take arguments as a map. Each is compared
			// against HTTP rather than against the other: neither shares a wire
			// format with anything, and what they do share is the dispatch
			// underneath, which is what these assertions are about.
			mapped := map[string]conformance.Outcome{
				"mcpx": callTool(t, session, tc.spec, tc.args),
				"adkx": callADK(t, adkTools, tc.spec, tc.args),
			}

			for _, name := range []string{"adkx", "mcpx"} {
				out := mapped[name]
				if out.Leaked {
					t.Errorf("%s leaked the internal error text: %s", name, out.Wire)
				}

				// Across transports, only what every transport can express.
				if std.Failed != out.Failed {
					t.Errorf("outcome diverges: HTTP failed=%v, %s failed=%v\n  http: %s\n  %s: %s",
						std.Failed, name, out.Failed, std.Wire, name, out.Wire)
				}
				if !std.Failed && !reflect.DeepEqual(std.Value, out.Value) {
					t.Errorf("success value diverges:\n  http: %#v\n  %s: %#v",
						std.Value, name, out.Value)
				}
				// Messages are compared only where every transport is reporting
				// the same thing to the same audience: a rejected input, built
				// by all of them from one *services.ValidationError. A refusal
				// or an internal fault is deliberately worded differently --
				// HTTP answers a client with a status and a sentence, the other
				// two answer a model with prose it can act on -- and asserting
				// those match would be asserting that a design decision had not
				// been taken.
				if std.Status == 400 && !sameMessages(std.Messages, out.Messages) {
					t.Errorf("rejection messages diverge:\n  http: %v\n  %s: %v",
						sorted(std.Messages), name, sorted(out.Messages))
				}
			}
		})
	}
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func sameMessages(a, b []string) bool { return reflect.DeepEqual(sorted(a), sorted(b)) }
