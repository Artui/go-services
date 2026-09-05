package httpx_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Artui/go-services"
	"github.com/Artui/go-services/httpx"
)

// A Location is filled from the operation's output by JSON name, and the
// filling itself is the kernel's -- so what this file asserts is the wiring:
// that the template reaches it, that the header lands, and that it does not
// land on any answer that is not a success.
func TestLocationIsFilledFromTheOutput(t *testing.T) {
	mux := mustMount(t, map[string]httpx.Route{
		"create_author": {
			Method: "POST", Pattern: "/authors", Location: "/authors/{id}",
		},
	}, httpx.Anonymous)

	rec := serve(mux, http.MethodPost, "/authors", strings.NewReader(`{"id":7,"name":"Ada"}`))
	if got := rec.Header().Get("Location"); got != "/authors/7" {
		t.Errorf("Location = %q, want %q", got, "/authors/7")
	}
}

// A value carrying a slash would forge a path segment if it were not escaped.
// The escaping is the kernel's; this proves the adapter does not undo it by
// building the header some other way.
func TestLocationEscapesTheValueItInterpolates(t *testing.T) {
	mux := mustMount(t, map[string]httpx.Route{
		"create_author": {
			Method: "POST", Pattern: "/authors", Location: "/authors/{name}",
		},
	}, httpx.Anonymous)

	rec := serve(mux, http.MethodPost, "/authors", strings.NewReader(`{"id":1,"name":"a/b"}`))
	if got := rec.Header().Get("Location"); got != "/authors/a%2Fb" {
		t.Errorf("Location = %q, want the slash escaped", got)
	}
}

func TestNoLocationTemplateSendsNoHeader(t *testing.T) {
	mux := mustMount(t, map[string]httpx.Route{
		"create_author": {Method: "POST", Pattern: "/authors"},
	}, httpx.Anonymous)

	rec := serve(mux, http.MethodPost, "/authors", strings.NewReader(`{"id":7,"name":"Ada"}`))
	if _, ok := rec.Header()["Location"]; ok {
		t.Errorf("Location = %q, want no header at all", rec.Header().Get("Location"))
	}
}

// Route.Location is the per-route expression of WithLocation and wins, exactly
// as Route.Status does over WithStatus.
func TestRouteLocationBeatsTheMountOption(t *testing.T) {
	mux := mustMount(t, map[string]httpx.Route{
		"create_author": {
			Method: "POST", Pattern: "/authors", Location: "/authors/{id}",
		},
	}, httpx.Anonymous, httpx.WithLocation("/wrong/{id}"))

	rec := serve(mux, http.MethodPost, "/authors", strings.NewReader(`{"id":7,"name":"Ada"}`))
	if got := rec.Header().Get("Location"); got != "/authors/7" {
		t.Errorf("Location = %q, want the route's template to win", got)
	}
}

// The bare-handler form, for a router Mount never saw.
func TestWithLocationOnAHandler(t *testing.T) {
	h := mustHandler(t, "create_author", httpx.Anonymous, httpx.WithLocation("/authors/{id}"))

	rec := serve(h, http.MethodPost, "/authors", strings.NewReader(`{"id":7,"name":"Ada"}`))
	if got := rec.Header().Get("Location"); got != "/authors/7" {
		t.Errorf("Location = %q, want %q", got, "/authors/7")
	}
}

// Nothing was created, so there is nowhere to point at.
func TestAFailureCarriesNoLocation(t *testing.T) {
	h := mustHandler(t, "create_author", httpx.Anonymous, httpx.WithLocation("/authors/{id}"))

	rec := serve(h, http.MethodPost, "/authors", strings.NewReader(`{"id":0,"name":"  "}`))
	if rec.Code < 400 {
		t.Fatalf("status = %d, want a refusal", rec.Code)
	}
	if _, ok := rec.Header()["Location"]; ok {
		t.Errorf("Location = %q on a %d", rec.Header().Get("Location"), rec.Code)
	}
}

// A 204 carries no body, and the header still has to be written before the
// status line -- a separate path through the writer from the one above.
func TestAStatusWithNoBodyStillCarriesLocation(t *testing.T) {
	h := mustHandler(t, "delete_author", httpx.Anonymous, httpx.WithLocation("/authors"))

	rec := serve(h, http.MethodDelete, "/authors/3", strings.NewReader(`{"id":3}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/authors" {
		t.Errorf("Location = %q, want it sent under a bodiless status", got)
	}
}

// The one case where a Location is built and then has to be taken back.
//
// A template with no placeholders never marshals the output to build itself, so
// it succeeds for a value the response body then cannot encode. The answer has
// become an internal error and must not still claim something was created.
func TestAnUnencodableValueDropsTheLocationItAlreadyBuilt(t *testing.T) {
	h := mustHandler(t, "unencodable", httpx.Anonymous, httpx.WithLocation("/fixed"))

	rec := serve(h, http.MethodGet, "/boom", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if _, ok := rec.Header()["Location"]; ok {
		t.Errorf("Location = %q on a 500", rec.Header().Get("Location"))
	}
}

// A broken template is a deployment fault, so it is refused when the handler is
// built rather than found by whoever followed the header.
func TestABrokenLocationTemplateIsRefusedAtConstruction(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
		says     string
	}{
		{"a field the output does not declare", "/authors/{nope}", "nope"},
		{"a placeholder that never closes", "/authors/{id", "never closes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := httpx.Handler(
				newRegistry(), "create_author", httpx.Anonymous, httpx.WithLocation(tc.template))
			if err == nil {
				t.Fatal("Handler accepted a broken Location template")
			}
			if !errors.Is(err, services.ErrConfiguration) {
				t.Errorf("err = %v, want ErrConfiguration", err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("err = %v, want it to mention %q", err, tc.says)
			}
		})
	}
}

// And Mount refuses the whole table rather than mounting the rest of it.
func TestMountRefusesABrokenLocationTemplate(t *testing.T) {
	err := httpx.Mount(http.NewServeMux(), newRegistry(), map[string]httpx.Route{
		"create_author": {Method: "POST", Pattern: "/authors", Location: "/authors/{nope}"},
	}, httpx.Anonymous)

	if err == nil {
		t.Fatal("Mount accepted a broken Location template")
	}
	if !errors.Is(err, services.ErrConfiguration) {
		t.Errorf("err = %v, want ErrConfiguration", err)
	}
}

// The two checks are not redundant, and this is the gap between them.
//
// CheckLocation reads the output SCHEMA, so it can only see whether a property
// is declared. "tags" is declared -- as an array, which no path segment can
// carry -- so the template passes at construction and fails when a response is
// actually built from it. The answer is a redacted 500, because a route
// producing a header nothing can follow is a deployment fault rather than
// anything the caller did.
func TestALocationThatOnlyFailsAtRequestTime(t *testing.T) {
	var observed error
	h, err := httpx.Handler(newRegistry(), "list_authors", httpx.Anonymous,
		httpx.WithLocation("/authors/{tags}"),
		httpx.WithOnError(func(_ *http.Request, _ int, err error) { observed = err }))
	if err != nil {
		t.Fatalf("Handler: the schema declares tags, so this must pass here: %v", err)
	}

	rec := serve(h, http.MethodGet, "/authors?tags=a&tags=b", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if _, ok := rec.Header()["Location"]; ok {
		t.Errorf("Location = %q, want none", rec.Header().Get("Location"))
	}
	// Redacted to the client, reported to the observer -- otherwise the fault
	// is invisible from both ends.
	if !strings.Contains(rec.Body.String(), services.InternalErrorText) {
		t.Errorf("body = %s, want the fixed sentence", rec.Body.String())
	}
	if !errors.Is(observed, services.ErrConfiguration) {
		t.Errorf("observed = %v, want ErrConfiguration", observed)
	}
	if !strings.Contains(observed.Error(), "an array") {
		t.Errorf("observed = %v, want it to say what was wrong", observed)
	}
}
