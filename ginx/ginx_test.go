// The tests are an external package on purpose: everything they reach for is
// part of the API a consumer has, so a test that needs an unexported symbol is
// a signal that the surface is missing something rather than a reason to widen
// the package clause.
package ginx_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Artui/go-services"
	"github.com/Artui/go-services/ginx"
	"github.com/gin-gonic/gin"
)

func TestMain(m *testing.M) {
	// Gin's debug mode writes route tables and warnings to stderr on every
	// engine, which would bury a real failure.
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// deps is the per-call dependency value. It carries the principal so a test can
// prove the opaque any this package handed the kernel came back out typed.
type deps struct{ user string }

func resolve(_ context.Context, principal any) (deps, error) {
	who, _ := principal.(string)
	return deps{user: who}, nil
}

type authorRef struct {
	ID int64 `json:"id"`
}

type author struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type listIn struct {
	Limit int      `json:"limit,omitempty"`
	Tags  []string `json:"tags,omitempty"`
}

type createIn struct {
	Name string `json:"name"`
}

// Validate is the kernel's second validation layer. It is here to prove that a
// per-field message raised below the transport still reaches the wire with its
// field intact.
func (in createIn) Validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return services.Invalid("name", "must not be blank")
	}
	return nil
}

type created struct {
	Name string `json:"name"`
	By   string `json:"by"`
}

type scope struct {
	Tenant string `json:"tenant"`
	Note   string `json:"note,omitempty"`
}

type nothing struct{}

// stats exists to carry a float64 that JSON cannot represent. NaN is not a
// contrived value here: it is what an average over no rows is.
type stats struct {
	Mean float64 `json:"mean"`
}

// operatorText is the sort of thing an unexpected error says: a host, a port
// and a reason, all of it addressed to whoever runs the service. No test may
// find it in a response body.
const operatorText = "dial tcp 10.0.0.5:5432: connection refused"

// newRegistry builds the registry every test serves. One registry with one spec
// per behaviour beats a bespoke one per test: the route tables below then read
// like a route table.
func newRegistry(t *testing.T) *services.Registry[deps] {
	t.Helper()
	reg := services.New(resolve)

	services.MustRegister(reg, services.Spec[deps, authorRef, author]{
		Name: "get_author", Kind: services.Query,
		Run: func(_ services.Ctx[deps], in authorRef) (author, error) {
			if in.ID != 1 {
				return author{}, fmt.Errorf("%w: author %d", services.ErrNotFound, in.ID)
			}
			return author{ID: 1, Name: "ada"}, nil
		},
	})

	services.MustRegister(reg, services.Spec[deps, listIn, listIn]{
		Name: "list_authors", Kind: services.Query,
		// Echoes its input back so a test can read what the query string was
		// coerced into rather than infer it.
		Run: func(_ services.Ctx[deps], in listIn) (listIn, error) { return in, nil },
	})

	services.MustRegister(reg, services.Spec[deps, createIn, created]{
		Name: "create_author", Kind: services.Mutation, Status: http.StatusCreated,
		Run: func(c services.Ctx[deps], in createIn) (created, error) {
			return created{Name: in.Name, By: c.Deps.user}, nil
		},
	})

	services.MustRegister(reg, services.Spec[deps, authorRef, nothing]{
		Name: "retire_author", Kind: services.Mutation, Status: http.StatusNoContent,
		Run: func(_ services.Ctx[deps], _ authorRef) (nothing, error) { return nothing{}, nil },
	})

	services.MustRegister(reg, services.Spec[deps, scope, scope]{
		Name: "note_scope", Kind: services.Mutation,
		Run: func(_ services.Ctx[deps], in scope) (scope, error) { return in, nil },
	})

	services.MustRegister(reg, services.Spec[deps, authorRef, author]{
		Name: "refuse", Kind: services.Query,
		Run: func(_ services.Ctx[deps], _ authorRef) (author, error) {
			return author{}, fmt.Errorf("%w: not yours", services.ErrPermission)
		},
	})

	services.MustRegister(reg, services.Spec[deps, authorRef, author]{
		Name: "clash", Kind: services.Mutation,
		Run: func(_ services.Ctx[deps], _ authorRef) (author, error) {
			return author{}, fmt.Errorf("%w: already retired", services.ErrConflict)
		},
	})

	services.MustRegister(reg, services.Spec[deps, authorRef, author]{
		// A ValidationError carrying no fields at all. Fields is an exported
		// field on a constructible struct, so this reaches a renderer sooner or
		// later and the renderer has to have an answer for it.
		Name: "vague", Kind: services.Query,
		Run: func(_ services.Ctx[deps], _ authorRef) (author, error) {
			return author{}, &services.ValidationError{}
		},
	})

	services.MustRegister(reg, services.Spec[deps, listIn, stats]{
		Name: "average", Kind: services.Query,
		Run: func(_ services.Ctx[deps], _ listIn) (stats, error) {
			return stats{Mean: math.NaN()}, nil
		},
	})

	services.MustRegister(reg, services.Spec[deps, authorRef, author]{
		Name: "boom", Kind: services.Query,
		Run: func(_ services.Ctx[deps], _ authorRef) (author, error) {
			return author{}, errors.New(operatorText)
		},
	})

	return reg
}

// staticPrincipal authenticates every request as the same person, which is all
// any test here needs from authentication.
func staticPrincipal(*gin.Context) (any, error) { return "ada", nil }

// engineFor mounts routes and returns the engine, failing the test if the mount
// is refused. Tests about refusal call ginx.Mount themselves.
func engineFor(t *testing.T, routes map[string]ginx.Route, opts ...ginx.Option) *gin.Engine {
	t.Helper()
	e := gin.New()
	if err := ginx.Mount(e, newRegistry(t), routes, staticPrincipal, opts...); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return e
}

// do issues one request against e. body may be nil for a request without one.
func do(e *gin.Engine, method, target string, body io.Reader) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(method, target, body))
	return rec
}

// bodyOf returns a nil io.Reader for the empty string rather than a non-nil
// interface holding a nil pointer, which httptest.NewRequest would try to read.
func bodyOf(s string) io.Reader {
	if s == "" {
		return nil
	}
	return strings.NewReader(s)
}

// assertJSON fails unless the recorded response has this status and exactly
// this body. Comparing the whole body rather than a field is deliberate: the
// envelope is a contract shared with the other HTTP adapter, and a test that
// only checked one key would not notice a second one appearing.
func assertJSON(t *testing.T, rec *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d (body %s)", rec.Code, status, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != body {
		t.Errorf("body = %s, want %s", got, body)
	}
}
