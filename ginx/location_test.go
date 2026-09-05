package ginx_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Artui/go-services"
	"github.com/Artui/go-services/ginx"
	"github.com/gin-gonic/gin"
)

// The filling is the kernel's, so what these assert is the wiring -- and,
// through the conformance suite, that this adapter and the net/http one build
// the same header from the same output.
func TestLocationIsFilledFromTheOutput(t *testing.T) {
	e := engineFor(t, map[string]ginx.Route{
		"create_author": {Method: "POST", Path: "/authors", Location: "/authors/{name}"},
	})

	rec := do(e, http.MethodPost, "/authors", bodyOf(`{"name":"ada"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/authors/ada" {
		t.Errorf("Location = %q, want %q", got, "/authors/ada")
	}
}

// A value carrying a slash would forge a path segment. The escaping is the
// kernel's; this proves the adapter does not build the header some other way.
func TestLocationEscapesTheValueItInterpolates(t *testing.T) {
	e := engineFor(t, map[string]ginx.Route{
		"create_author": {Method: "POST", Path: "/authors", Location: "/authors/{name}"},
	})

	rec := do(e, http.MethodPost, "/authors", bodyOf(`{"name":"a/b"}`))
	if got := rec.Header().Get("Location"); got != "/authors/a%2Fb" {
		t.Errorf("Location = %q, want the slash escaped", got)
	}
}

func TestNoLocationTemplateSendsNoHeader(t *testing.T) {
	e := engineFor(t, map[string]ginx.Route{
		"create_author": {Method: "POST", Path: "/authors"},
	})

	rec := do(e, http.MethodPost, "/authors", bodyOf(`{"name":"ada"}`))
	if _, ok := rec.Header()["Location"]; ok {
		t.Errorf("Location = %q, want no header at all", rec.Header().Get("Location"))
	}
}

// Route.Location is the per-route expression of WithLocation and wins, exactly
// as Route.Status does over WithStatus.
func TestRouteLocationBeatsTheMountOption(t *testing.T) {
	e := engineFor(t, map[string]ginx.Route{
		"create_author": {Method: "POST", Path: "/authors", Location: "/authors/{name}"},
	}, ginx.WithLocation("/wrong/{name}"))

	rec := do(e, http.MethodPost, "/authors", bodyOf(`{"name":"ada"}`))
	if got := rec.Header().Get("Location"); got != "/authors/ada" {
		t.Errorf("Location = %q, want the route's template to win", got)
	}
}

// A mount option with no route override still reaches every route in the table.
func TestMountLocationAppliesToTheWholeTable(t *testing.T) {
	e := engineFor(t, map[string]ginx.Route{
		"create_author": {Method: "POST", Path: "/authors"},
	}, ginx.WithLocation("/authors/{name}"))

	rec := do(e, http.MethodPost, "/authors", bodyOf(`{"name":"ada"}`))
	if got := rec.Header().Get("Location"); got != "/authors/ada" {
		t.Errorf("Location = %q, want the mount option to apply", got)
	}
}

// Nothing was created, so there is nowhere to point at.
func TestAFailureCarriesNoLocation(t *testing.T) {
	e := engineFor(t, map[string]ginx.Route{
		"refuse": {Method: "GET", Path: "/refuse/:id", Location: "/authors/{name}"},
	})

	rec := do(e, http.MethodGet, "/refuse/1", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if _, ok := rec.Header()["Location"]; ok {
		t.Errorf("Location = %q on a 403", rec.Header().Get("Location"))
	}
}

// A 204 carries no body and still carries the header.
func TestAStatusWithNoBodyStillCarriesLocation(t *testing.T) {
	e := engineFor(t, map[string]ginx.Route{
		"retire_author": {Method: "DELETE", Path: "/authors/:id", Location: "/authors"},
	})

	rec := do(e, http.MethodDelete, "/authors/1", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/authors" {
		t.Errorf("Location = %q, want it sent under a bodiless status", got)
	}
}

// A value the encoder cannot represent is a 500 with no Location. This adapter
// reaches that from the other direction than the net/http one -- it marshals
// first and never expands -- and the answer a client sees is the same, which is
// what the conformance suite exists to hold.
func TestAnUnencodableValueSendsNoLocation(t *testing.T) {
	e := engineFor(t, map[string]ginx.Route{
		"average": {Method: "GET", Path: "/average", Location: "/fixed"},
	})

	rec := do(e, http.MethodGet, "/average", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if _, ok := rec.Header()["Location"]; ok {
		t.Errorf("Location = %q on a 500", rec.Header().Get("Location"))
	}
}

// The gap between the two checks, and the reason they are not redundant.
//
// CheckLocation reads the output SCHEMA, so it sees only that "tags" is
// declared -- as an array, which no path segment can carry. The template passes
// at mount and fails when a response is built from it, answered as a redacted
// 500 because a route producing an unfollowable header is a deployment fault.
func TestALocationThatOnlyFailsAtRequestTime(t *testing.T) {
	var observed error
	e := engineFor(t, map[string]ginx.Route{
		"list_authors": {Method: "GET", Path: "/authors", Location: "/authors/{tags}"},
	}, ginx.WithErrorHandler(func(_ *gin.Context, err error) { observed = err }))

	rec := do(e, http.MethodGet, "/authors?tags=a&tags=b", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	if _, ok := rec.Header()["Location"]; ok {
		t.Errorf("Location = %q, want none", rec.Header().Get("Location"))
	}
	if !errors.Is(observed, services.ErrConfiguration) {
		t.Errorf("observed = %v, want ErrConfiguration", observed)
	}
	if !strings.Contains(observed.Error(), "an array") {
		t.Errorf("observed = %v, want it to say what was wrong", observed)
	}
}

// A broken template is refused where the handler is built.
func TestABrokenLocationTemplateIsRefusedAtMount(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
		says     string
	}{
		{"a field the output does not declare", "/authors/{nope}", "nope"},
		{"a placeholder that never closes", "/authors/{name", "never closes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ginx.Mount(gin.New(), newRegistry(t), map[string]ginx.Route{
				"create_author": {Method: "POST", Path: "/authors", Location: tc.template},
			}, staticPrincipal)

			if err == nil {
				t.Fatal("Mount accepted a broken Location template")
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

// And the bare-handler form refuses it too, for a router Mount never saw.
func TestHandlerRefusesABrokenLocationTemplate(t *testing.T) {
	_, err := ginx.Handler(newRegistry(t), "create_author", staticPrincipal,
		ginx.WithLocation("/authors/{nope}"))
	if err == nil {
		t.Fatal("Handler accepted a broken Location template")
	}
	if !errors.Is(err, services.ErrConfiguration) {
		t.Errorf("err = %v, want ErrConfiguration", err)
	}
}
