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

func TestMountRefusesBadConfiguration(t *testing.T) {
	reg := newRegistry(t)
	fine := map[string]ginx.Route{"get_author": {Method: "GET", Path: "/authors/:id"}}

	for _, tc := range []struct {
		name string
		call func(*gin.Engine) error
		want string
	}{
		{
			name: "a nil router",
			call: func(*gin.Engine) error { return ginx.Mount(nil, reg, fine, staticPrincipal) },
			want: "needs a router",
		},
		{
			name: "a nil registry",
			call: func(e *gin.Engine) error {
				return ginx.Mount[deps](e, nil, fine, staticPrincipal)
			},
			want: "needs a registry",
		},
		{
			name: "a missing principal function",
			call: func(e *gin.Engine) error { return ginx.Mount(e, reg, fine, nil) },
			want: "ginx.Anonymous",
		},
		{
			name: "an empty table",
			call: func(e *gin.Engine) error {
				return ginx.Mount(e, reg, map[string]ginx.Route{}, staticPrincipal)
			},
			want: "no routes",
		},
		{
			name: "an option no response can carry",
			call: func(e *gin.Engine) error {
				return ginx.Mount(e, reg, fine, staticPrincipal, ginx.WithStatus(999))
			},
			want: "999 is not an HTTP status code",
		},
		{
			name: "a route status no response can carry",
			call: func(e *gin.Engine) error {
				return ginx.Mount(e, reg, map[string]ginx.Route{
					"get_author": {Method: "GET", Path: "/authors/:id", Status: 42},
				}, staticPrincipal)
			},
			want: "42 is not an HTTP status code",
		},
		{
			name: "a name the registry does not know",
			call: func(e *gin.Engine) error {
				return ginx.Mount(e, reg, map[string]ginx.Route{
					"no_such_spec": {Method: "GET", Path: "/nope"},
				}, staticPrincipal)
			},
			want: `no spec named "no_such_spec"`,
		},
		{
			name: "a method no spec can be served on",
			call: func(e *gin.Engine) error {
				return ginx.Mount(e, reg, map[string]ginx.Route{
					"get_author": {Method: "SNIFF", Path: "/authors/:id"},
				}, staticPrincipal)
			},
			want: "cannot be mounted on SNIFF",
		},
		{
			name: "a route with no path",
			call: func(e *gin.Engine) error {
				return ginx.Mount(e, reg, map[string]ginx.Route{
					"get_author": {Method: "GET"},
				}, staticPrincipal)
			},
			want: `"get_author" has no path`,
		},
		{
			name: "two specs on the same method and path",
			call: func(e *gin.Engine) error {
				return ginx.Mount(e, reg, map[string]ginx.Route{
					"get_author": {Method: "GET", Path: "/authors/:id"},
					"refuse":     {Method: "GET", Path: "/authors/:id"},
				}, staticPrincipal)
			},
			want: `"get_author" and "refuse" both claim GET /authors/:id`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := gin.New()
			err := tc.call(e)
			if err == nil {
				t.Fatal("got no error, want a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
			// The router must be untouched. A Mount that registered the rows it
			// liked before reaching the one it did not would start an
			// application that serves most of its API and 404s the rest.
			if routes := e.Routes(); len(routes) != 0 {
				t.Errorf("a refused Mount registered %v", routes)
			}
		})
	}
}

// Every problem is reported, in a stable order, because a route table is
// written by hand and finding its mistakes one restart at a time is the worst
// possible way to spend a morning.
func TestMountReportsEveryProblem(t *testing.T) {
	err := ginx.Mount(gin.New(), newRegistry(t), map[string]ginx.Route{
		"aaa_missing": {Method: "GET", Path: "/a"},
		"zzz_missing": {Method: "GET", Path: "/z"},
		"get_author":  {Method: "POST", Path: "/authors/:id"},
	}, staticPrincipal)
	if err == nil {
		t.Fatal("got no error, want three refusals")
	}
	msg := err.Error()
	for _, want := range []string{"aaa_missing", "zzz_missing", "cannot be mounted on POST"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to mention %q", msg, want)
		}
	}
	// Sorted by spec name, so the same broken table reads the same way twice.
	if strings.Index(msg, "aaa_missing") > strings.Index(msg, "zzz_missing") {
		t.Errorf("error = %q, want the problems in spec-name order", msg)
	}
}

// Kind is declared on the spec, so which methods it may be served on is a
// question that can be answered before a request exists. The answer itself is
// services.Kind.AllowsMethod: this package asks rather than decides, so that
// the two HTTP adapters cannot drift into two different rules.
func TestMountChecksKindAgainstMethod(t *testing.T) {
	for _, tc := range []struct {
		name, spec, method string
		refused            bool
	}{
		{name: "a query on GET", spec: "get_author", method: "GET"},
		{name: "a query on HEAD", spec: "get_author", method: "HEAD"},
		{name: "a query on OPTIONS", spec: "get_author", method: "OPTIONS"},
		{name: "a query on POST", spec: "get_author", method: "POST", refused: true},
		{name: "a query on PUT", spec: "get_author", method: "PUT", refused: true},
		{name: "a query on PATCH", spec: "get_author", method: "PATCH", refused: true},
		// A DELETE that has no side effects is a lie about the route, and it
		// used to get through: the rule was once two prohibitions written in
		// terms of "carries a body" and "is GET", and neither reached this.
		{name: "a query on DELETE", spec: "get_author", method: "DELETE", refused: true},

		{name: "a mutation on POST", spec: "retire_author", method: "POST"},
		{name: "a mutation on PUT", spec: "retire_author", method: "PUT"},
		{name: "a mutation on PATCH", spec: "retire_author", method: "PATCH"},
		{name: "a mutation on DELETE", spec: "retire_author", method: "DELETE"},
		{name: "a mutation on GET", spec: "retire_author", method: "GET", refused: true},
		// The other half of the same closed gap: HEAD is safe, and a write
		// reached by one returns no body to say what it did.
		{name: "a mutation on HEAD", spec: "retire_author", method: "HEAD", refused: true},
		{name: "a mutation on OPTIONS", spec: "retire_author", method: "OPTIONS", refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ginx.Mount(gin.New(), newRegistry(t), map[string]ginx.Route{
				tc.spec: {Method: tc.method, Path: "/thing/:id"},
			}, staticPrincipal)
			if tc.refused && err == nil {
				t.Fatalf("%s on %s was accepted", tc.spec, tc.method)
			}
			if !tc.refused && err != nil {
				t.Fatalf("%s on %s was refused: %v", tc.spec, tc.method, err)
			}
		})
	}
}

func TestMountAcceptsAnyCasingOfAMethod(t *testing.T) {
	e := engineFor(t, map[string]ginx.Route{
		"get_author": {Method: " get ", Path: "/authors/:id"},
	})
	assertJSON(t, do(e, http.MethodGet, "/authors/1", nil), http.StatusOK, `{"id":1,"name":"ada"}`)
}

// A Route.Status overrides the spec's, and a WithStatus passed to Mount is the
// default for the rows that did not say. The two must not leak into each other:
// the override for one route is built by appending to the caller's option
// slice, which is exactly the shape that quietly writes into a shared array.
func TestStatusPrecedence(t *testing.T) {
	e := engineFor(t, map[string]ginx.Route{
		"create_author": {Method: "POST", Path: "/authors", Status: http.StatusTeapot},
		"note_scope":    {Method: "PUT", Path: "/tenants/:tenant/notes"},
	}, ginx.WithStatus(http.StatusAccepted))

	assertJSON(t, do(e, http.MethodPost, "/authors", strings.NewReader(`{"name":"grace"}`)),
		http.StatusTeapot, `{"name":"grace","by":"ada"}`)
	assertJSON(t, do(e, http.MethodPut, "/tenants/acme/notes", nil),
		http.StatusAccepted, `{"tenant":"acme"}`)
}

// Mounting is what an application does at startup, so the whole table has to be
// reachable afterwards rather than most of it.
func TestMountRegistersEveryRoute(t *testing.T) {
	e := engineFor(t, authorRoutes())
	got := map[string]bool{}
	for _, r := range e.Routes() {
		got[r.Method+" "+r.Path] = true
	}
	for _, want := range []string{
		"GET /authors/:id", "GET /authors", "POST /authors", "DELETE /authors/:id",
		"PUT /tenants/:tenant/notes", "GET /refuse/:id", "POST /clash", "GET /boom/:id",
	} {
		if !got[want] {
			t.Errorf("%s was not registered; got %v", want, got)
		}
	}
}

// A group is the reason Mount takes gin.IRouter rather than *gin.Engine: an
// application mounts its services under a prefix, behind its own middleware.
func TestMountOntoAGroup(t *testing.T) {
	e := gin.New()
	api := e.Group("/api/v1")
	err := ginx.Mount(api, newRegistry(t), map[string]ginx.Route{
		"get_author": {Method: "GET", Path: "/authors/:id"},
	}, staticPrincipal)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	assertJSON(t, do(e, http.MethodGet, "/api/v1/authors/1", nil),
		http.StatusOK, `{"id":1,"name":"ada"}`)
}

// The registry is the source of the route table's vocabulary, so a spec added
// by a view is mountable exactly as the spec it came from.
func TestMountAView(t *testing.T) {
	reg := services.New(resolve)
	services.MustRegister(reg, services.Spec[deps, authorRef, author]{
		Name: "get_author", Kind: services.Query, Tags: []string{"public"},
		Run: func(_ services.Ctx[deps], _ authorRef) (author, error) {
			return author{ID: 1, Name: "ada"}, nil
		},
	})
	services.MustRegister(reg, services.Spec[deps, authorRef, author]{
		Name: "secret", Kind: services.Query,
		Run: func(_ services.Ctx[deps], _ authorRef) (author, error) { return author{}, nil },
	})

	e := gin.New()
	err := ginx.Mount(e, reg.ByTag("public"), map[string]ginx.Route{
		"get_author": {Method: "GET", Path: "/authors/:id"},
	}, staticPrincipal)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if err := ginx.Mount(e, reg.ByTag("public"), map[string]ginx.Route{
		"secret": {Method: "GET", Path: "/secret/:id"},
	}, staticPrincipal); err == nil {
		t.Error("a spec outside the view was mountable from it")
	}
}

// A route table naming a capture the operation cannot receive is broken in
// every request it will ever serve, so it is refused before it serves any.
func TestMountChecksCapturesAgainstTheSchema(t *testing.T) {
	for _, tc := range []struct {
		name, spec, method, path string
		refused                  bool
	}{
		{
			name: "a capture naming a declared field",
			spec: "note_scope", method: "PUT", path: "/tenants/:tenant/notes",
		},
		{
			name: "a catch-all naming a declared field",
			spec: "note_scope", method: "PUT", path: "/notes/*tenant",
		},
		{
			name: "a route with no captures at all",
			spec: "list_authors", method: "GET", path: "/authors",
		},
		{
			name: "a capture naming nothing",
			spec: "list_authors", method: "GET", path: "/tenants/:tenant/authors",
			refused: true,
		},
		{
			name: "a catch-all naming nothing",
			spec: "list_authors", method: "GET", path: "/authors/*region",
			refused: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := gin.New()
			err := ginx.Mount(e, newRegistry(t), map[string]ginx.Route{
				tc.spec: {Method: tc.method, Path: tc.path},
			}, staticPrincipal)

			if !tc.refused {
				if err != nil {
					t.Fatalf("%s was refused: %v", tc.path, err)
				}
				return
			}
			if err == nil {
				t.Fatal("got no error, want a refusal")
			}
			// The kernel's own taxonomy, so an application starting up can tell
			// a broken route table from a broken registry.
			if !errors.Is(err, services.ErrConfiguration) {
				t.Errorf("error = %v, want a configuration error", err)
			}
			if routes := e.Routes(); len(routes) != 0 {
				t.Errorf("a refused Mount registered %v", routes)
			}
		})
	}
}

// Every undeclared capture is named at once and in sorted order, so a route
// with two mistakes takes one restart rather than two.
func TestMountNamesEveryUndeclaredCapture(t *testing.T) {
	err := ginx.Mount(gin.New(), newRegistry(t), map[string]ginx.Route{
		"list_authors": {Method: "GET", Path: "/x/:zebra/y/:apple/authors"},
	}, staticPrincipal)
	if err == nil {
		t.Fatal("got no error, want a refusal")
	}
	msg := err.Error()
	if !strings.Contains(msg, "apple") || !strings.Contains(msg, "zebra") {
		t.Fatalf("error = %q, want both captures named", msg)
	}
	if strings.Index(msg, "apple") > strings.Index(msg, "zebra") {
		t.Errorf("error = %q, want the captures in sorted order", msg)
	}
}
