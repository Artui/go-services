package httpx_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/httpx"
)

// mustHandler builds a Handler or fails the test. A construction error is a
// configuration bug in the test itself, never the thing under test here.
func mustHandler(t *testing.T, name string, principal httpx.Principal, opts ...httpx.Option) http.Handler {
	t.Helper()
	h, err := httpx.Handler(newRegistry(), name, principal, opts...)
	if err != nil {
		t.Fatalf("Handler(%q): %v", name, err)
	}
	return h
}

// mustMount mounts routes on a fresh mux or fails the test.
func mustMount(
	t *testing.T, routes map[string]httpx.Route, principal httpx.Principal, opts ...httpx.Option,
) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	if err := httpx.Mount(mux, newRegistry(), routes, principal, opts...); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return mux
}

// serve runs one request through h and returns the recorder.
func serve(h http.Handler, method, target string, body io.Reader) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, body))
	return rec
}

// decode reads the recorded body into v, failing the test if it is not JSON.
func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
}

func TestHandlerServesTheSpecValue(t *testing.T) {
	h := mustHandler(t, "get_author", func(*http.Request) (any, error) { return "ada", nil })

	rec := serve(h, http.MethodGet, "/?id=7&verbose=true", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var out authorOut
	decode(t, rec, &out)
	// The query string is all strings on the wire; that id came back as a
	// number and verbose as a bool is the kernel's schema-directed coercion,
	// which this adapter deliberately does not reimplement.
	if out != (authorOut{ID: 7, Viewer: "ada", Verbose: true}) {
		t.Errorf("out = %+v", out)
	}
}

// Anonymous is how a mount says out loud that it authenticates nobody.
func TestAnonymousDispatchesANilPrincipal(t *testing.T) {
	h := mustHandler(t, "ping", httpx.Anonymous)

	rec := serve(h, http.MethodGet, "/", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != `"pong"` {
		t.Errorf("body = %s", got)
	}
}

// A forgotten Principal and a deliberately public one look identical in a call,
// and only one of them was meant. The nil is refused so they cannot be.
func TestHandlerRefusesANilPrincipal(t *testing.T) {
	_, err := httpx.Handler(newRegistry(), "ping", nil)

	if err == nil {
		t.Fatal("want an error for a nil principal")
	}
	if !strings.Contains(err.Error(), "httpx.Anonymous") {
		t.Errorf("error = %v, want it to name the way to mount unauthenticated", err)
	}
}

func TestHandlerRepeatedQueryKeyBecomesAnArray(t *testing.T) {
	h := mustHandler(t, "list_authors", httpx.Anonymous)

	rec := serve(h, http.MethodGet, "/?tags=go&tags=http&limit=5", nil)

	var out listOut
	decode(t, rec, &out)
	if out.Limit != 5 || strings.Join(out.Tags, ",") != "go,http" {
		t.Errorf("out = %+v", out)
	}
}

func TestHandlerReadsTheBodyOnAMutation(t *testing.T) {
	h := mustHandler(t, "create_author", func(*http.Request) (any, error) { return "ada", nil })

	rec := serve(h, http.MethodPost, "/", strings.NewReader(`{"id":1,"name":"Grace"}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	var out writeOut
	decode(t, rec, &out)
	if out != (writeOut{ID: 1, Name: "Grace", Viewer: "ada"}) {
		t.Errorf("out = %+v", out)
	}
}

// The precedence rule in one request: the same two keys arrive in all three
// positions at once. A route capture must win, or a client handed a link can
// rescope an operation the path had already scoped.
func TestHandlerPathBeatsQueryBeatsBody(t *testing.T) {
	mux := mustMount(t, map[string]httpx.Route{
		"create_author": {Method: http.MethodPost, Pattern: "/authors/{id}"},
	}, httpx.Anonymous)

	rec := serve(mux, http.MethodPost, "/authors/7?id=50&name=query",
		strings.NewReader(`{"id":99,"name":"body"}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var out writeOut
	decode(t, rec, &out)
	if out.ID != 7 {
		t.Errorf("id = %d, want 7 -- the route scope must win over a query string", out.ID)
	}
	if out.Name != "query" {
		t.Errorf("name = %q, want %q -- a parameter must win over the body", out.Name, "query")
	}
}

func TestHandlerServedOutsideAMuxHasNoCaptures(t *testing.T) {
	// Request.Pattern is empty unless a ServeMux matched, so the default
	// extractor must find nothing rather than guess.
	h := mustHandler(t, "get_author", httpx.Anonymous)

	rec := serve(h, http.MethodGet, "/authors/7", nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestHandlerStatusOverride(t *testing.T) {
	h := mustHandler(t, "get_author", httpx.Anonymous, httpx.WithStatus(http.StatusAccepted))

	rec := serve(h, http.MethodGet, "/?id=1", nil)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
}

func TestHandlerRejectsAnUnregisteredName(t *testing.T) {
	_, err := httpx.Handler(newRegistry(), "nope", httpx.Anonymous)
	if err == nil {
		t.Fatal("want an error for an unregistered name")
	}
	if !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("error = %v, want it to name the spec", err)
	}
}

func TestHandlerRejectsAnUnsendableStatus(t *testing.T) {
	for _, status := range []int{99, 600} {
		_, err := httpx.Handler(newRegistry(), "ping", httpx.Anonymous, httpx.WithStatus(status))
		if err == nil {
			t.Errorf("status %d: want an error", status)
		}
	}
}

func TestHandlerNoBodyForAStatusThatForbidsOne(t *testing.T) {
	// 204 comes from the spec itself; 304 from an override. net/http discards a
	// body under either, so writing one is invisible until a proxy complains.
	cases := map[string]struct {
		name string
		opts []httpx.Option
		want int
	}{
		"204 from the spec": {name: "delete_author", want: http.StatusNoContent},
		"304 from an override": {
			name: "ping",
			opts: []httpx.Option{httpx.WithStatus(http.StatusNotModified)},
			want: http.StatusNotModified,
		},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			h := mustHandler(t, tc.name, httpx.Anonymous, tc.opts...)

			rec := serve(h, http.MethodGet, "/?id=1", nil)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("body = %q, want empty", rec.Body)
			}
			if got := rec.Header().Get("Content-Type"); got != "" {
				t.Errorf("Content-Type = %q, want none on a body-less response", got)
			}
		})
	}
}

// The limit is the kernel's, so this test names the kernel's constant rather
// than a number of its own: there is no per-adapter knob to set.
func TestHandlerRefusesABodyOverTheLimit(t *testing.T) {
	var observed []int
	h := mustHandler(t, "create_author", httpx.Anonymous,
		httpx.WithOnError(func(_ *http.Request, status int, _ error) {
			observed = append(observed, status)
		}),
	)
	oversize := strings.NewReader(
		`{"id":1,"name":"` + strings.Repeat("a", int(services.DefaultMaxBodyBytes)) + `"}`,
	)

	rec := serve(h, http.MethodPost, "/", oversize)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != `{"error":"request body too large"}` {
		t.Errorf("body = %s", got)
	}
	if len(observed) != 1 || observed[0] != http.StatusRequestEntityTooLarge {
		t.Errorf("observed = %v, want one 413", observed)
	}
}

// failingReader stands in for a client that hangs up part-way through a body.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestHandlerUnreadableBodyIsAFourHundred(t *testing.T) {
	var observed []error
	h := mustHandler(t, "create_author", httpx.Anonymous,
		httpx.WithOnError(func(_ *http.Request, _ int, err error) {
			observed = append(observed, err)
		}),
	)

	rec := serve(h, http.MethodPost, "/", failingReader{})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	var body struct {
		Errors map[string][]string `json:"errors"`
	}
	decode(t, rec, &body)
	msgs := body.Errors[services.NonFieldKey]
	if len(msgs) != 1 || msgs[0] != "the request body could not be read" {
		t.Errorf("errors = %v, want the fixed whole-payload message", body.Errors)
	}
	// net/http's own wording describes the connection rather than anything the
	// client can act on, so it stays on the observer's side of the line.
	if len(observed) != 1 || !strings.Contains(observed[0].Error(), "the request body could not be read") {
		t.Errorf("observed = %v", observed)
	}
}

func TestHandlerCustomPathValues(t *testing.T) {
	// The shape a router with its own capture syntax uses. Everything
	// downstream is unchanged: the capture is still coerced by the schema and
	// still beats the query string.
	h := mustHandler(t, "get_author", httpx.Anonymous, httpx.WithPathValues(
		func(*http.Request) map[string][]string { return map[string][]string{"id": {"31"}} },
	))

	rec := serve(h, http.MethodGet, "/?id=4", nil)

	var out authorOut
	decode(t, rec, &out)
	if out.ID != 31 {
		t.Errorf("id = %d, want 31", out.ID)
	}
}

func TestHandlerNilPathValuesRestoresTheDefault(t *testing.T) {
	mux := http.NewServeMux()
	h := mustHandler(t, "get_author", httpx.Anonymous, httpx.WithPathValues(nil))
	mux.Handle("GET /authors/{id}", h)

	rec := serve(mux, http.MethodGet, "/authors/12", nil)

	var out authorOut
	decode(t, rec, &out)
	if out.ID != 12 {
		t.Errorf("id = %d, want 12 -- a nil extractor must not disable path binding", out.ID)
	}
}

func TestHandlerMultiSegmentCapture(t *testing.T) {
	mux := http.NewServeMux()
	h := mustHandler(t, "list_authors", httpx.Anonymous)
	// A "{rest...}" wildcard is read back under the name without the dots, and
	// {$} is not a capture at all.
	mux.Handle("GET /files/{tags...}", h)

	rec := serve(mux, http.MethodGet, "/files/a/b", nil)

	var out listOut
	decode(t, rec, &out)
	if strings.Join(out.Tags, "|") != "a/b" {
		t.Errorf("tags = %v", out.Tags)
	}
}

// One Handler under two patterns. Captures are read off the pattern the request
// matched, not off the one the handler was built for, so the same handler binds
// correctly under both.
func TestOneHandlerUnderTwoPatterns(t *testing.T) {
	mux := http.NewServeMux()
	h := mustHandler(t, "get_author", httpx.Anonymous)
	mux.Handle("GET /authors/{id}", h)
	mux.Handle("GET /v1/authors/{id}", h)

	for _, target := range []string{"/authors/5", "/v1/authors/5"} {
		var out authorOut
		decode(t, serve(mux, http.MethodGet, target, nil), &out)
		if out.ID != 5 {
			t.Errorf("%s: id = %d, want 5", target, out.ID)
		}
	}
}
