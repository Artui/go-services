package httpx_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Artui/go-services/httpx"
)

func TestMountRoutesEverySpecItNames(t *testing.T) {
	mux := mustMount(t, map[string]httpx.Route{
		"list_authors":  {Method: http.MethodGet, Pattern: "/authors"},
		"get_author":    {Method: http.MethodGet, Pattern: "/authors/{id}"},
		"create_author": {Method: http.MethodPost, Pattern: "/authors"},
		"delete_author": {Method: http.MethodDelete, Pattern: "/authors/{id}"},
	}, func(*http.Request) (any, error) { return "ada", nil })

	cases := []struct {
		method, target string
		body           string
		want           int
	}{
		{method: http.MethodGet, target: "/authors?limit=2", want: http.StatusOK},
		{method: http.MethodGet, target: "/authors/9", want: http.StatusOK},
		{method: http.MethodPost, target: "/authors", body: `{"id":1,"name":"Grace"}`, want: http.StatusCreated},
		{method: http.MethodDelete, target: "/authors/9", want: http.StatusNoContent},
	}
	for _, tc := range cases {
		var body io.Reader
		if tc.body != "" {
			body = strings.NewReader(tc.body)
		}
		rec := serve(mux, tc.method, tc.target, body)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d: %s", tc.method, tc.target, rec.Code, tc.want, rec.Body)
		}
	}

	// A registry may hold more specs than a mount routes: the rest are still
	// reachable over another transport, so an unrouted spec is not an error.
	if rec := serve(mux, http.MethodGet, "/ping", nil); rec.Code != http.StatusNotFound {
		t.Errorf("an unrouted spec answered on /ping with %d", rec.Code)
	}
}

// A method is trimmed and upper-cased before it reaches either ServeMux or the
// kernel's Kind check, so a route table written by hand can say "post".
func TestMountNormalisesTheMethod(t *testing.T) {
	mux := mustMount(t, map[string]httpx.Route{
		"create_author": {Method: " post ", Pattern: "/authors"},
	}, httpx.Anonymous)

	rec := serve(mux, http.MethodPost, "/authors", strings.NewReader(`{"id":1,"name":"Grace"}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
}

func TestMountRouteStatusOverridesTheSpec(t *testing.T) {
	mux := mustMount(t, map[string]httpx.Route{
		"create_author": {Method: http.MethodPost, Pattern: "/authors", Status: http.StatusOK},
	}, httpx.Anonymous)

	rec := serve(mux, http.MethodPost, "/authors", strings.NewReader(`{"id":1,"name":"Grace"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the route's 200 rather than the spec's 201", rec.Code)
	}
}

func TestMountPassesItsOptionsThrough(t *testing.T) {
	var observed []int
	mux := mustMount(t,
		map[string]httpx.Route{
			"gone":  {Method: http.MethodGet, Pattern: "/gone"},
			"taken": {Method: http.MethodGet, Pattern: "/taken"},
		},
		httpx.Anonymous,
		httpx.WithOnError(func(_ *http.Request, status int, _ error) {
			observed = append(observed, status)
		}),
	)

	serve(mux, http.MethodGet, "/gone", nil)
	serve(mux, http.MethodGet, "/taken", nil)

	if len(observed) != 2 || observed[0] != http.StatusNotFound || observed[1] != http.StatusConflict {
		t.Errorf("observed = %v, want [404 409] -- every handler must get the mount's options", observed)
	}
}

func TestMountRejectsBadConfiguration(t *testing.T) {
	cases := map[string]struct {
		routes    map[string]httpx.Route
		principal httpx.Principal
		want      string
	}{
		"a name that is not registered": {
			routes: map[string]httpx.Route{"nope": {Method: http.MethodGet, Pattern: "/nope"}},
			want:   `no spec named "nope"`,
		},
		"no method": {
			routes: map[string]httpx.Route{"ping": {Pattern: "/ping"}},
			want:   "must name a method",
		},
		"a method that is only whitespace": {
			routes: map[string]httpx.Route{"ping": {Method: "  ", Pattern: "/ping"}},
			want:   "must name a method",
		},
		"no pattern": {
			routes: map[string]httpx.Route{"ping": {Method: http.MethodGet}},
			want:   "must have a pattern",
		},
		"the method written into the pattern too": {
			routes: map[string]httpx.Route{"ping": {Method: http.MethodGet, Pattern: "GET /ping"}},
			want:   "must not contain the method",
		},
		"a status that cannot be sent": {
			routes: map[string]httpx.Route{"ping": {Method: http.MethodGet, Pattern: "/ping", Status: 42}},
			want:   "cannot be sent as a response status",
		},
		// The method rule is the kernel's; these assert this adapter surfaces
		// it, and that it covers the two cases a hand-written pair of
		// prohibitions had missed.
		"a mutation on GET": {
			routes: map[string]httpx.Route{"create_author": {Method: http.MethodGet, Pattern: "/authors"}},
			want:   "a mutation changes state and cannot be mounted on GET",
		},
		"a mutation on HEAD": {
			routes: map[string]httpx.Route{"create_author": {Method: http.MethodHead, Pattern: "/authors"}},
			want:   "cannot be mounted on HEAD",
		},
		"a query on POST": {
			routes: map[string]httpx.Route{"get_author": {Method: http.MethodPost, Pattern: "/authors"}},
			want:   "a query has no side effects and cannot be mounted on POST",
		},
		"a query on DELETE": {
			routes: map[string]httpx.Route{"get_author": {Method: http.MethodDelete, Pattern: "/authors"}},
			want:   "cannot be mounted on DELETE",
		},
		// A route naming a capture the operation cannot receive is broken in
		// every request it would ever serve, so it is refused before it serves
		// any. The kernel refuses it again at dispatch, for the handler that
		// never came through here.
		"a capture the input has no property for": {
			routes: map[string]httpx.Route{"get_author": {Method: http.MethodGet, Pattern: "/authors/{slug}"}},
			want:   "captures slug",
		},
		"a capture on an input with no properties at all": {
			routes: map[string]httpx.Route{"ping": {Method: http.MethodGet, Pattern: "/ping/{id}"}},
			want:   "captures id",
		},
		"every undeclared capture at once, sorted": {
			routes: map[string]httpx.Route{"ping": {Method: http.MethodGet, Pattern: "/ping/{zebra}/{alpha}"}},
			want:   "captures alpha, zebra",
		},
		"two specs on one method and path": {
			routes: map[string]httpx.Route{
				"list_authors": {Method: http.MethodGet, Pattern: "/shared"},
				"ping":         {Method: http.MethodGet, Pattern: "/shared"},
			},
			want: `"list_authors" and "ping" both claim "GET /shared"`,
		},
		"a pattern ServeMux cannot parse": {
			routes: map[string]httpx.Route{"ping": {Method: http.MethodGet, Pattern: "no-leading-slash"}},
			want:   "mounting",
		},
		"two patterns that overlap with neither more specific": {
			routes: map[string]httpx.Route{
				"get_author":   {Method: http.MethodGet, Pattern: "/a/{id}/c"},
				"list_authors": {Method: http.MethodGet, Pattern: "/a/b/{tags}"},
			},
			want: "conflicts with pattern",
		},
		"a missing principal": {
			routes:    map[string]httpx.Route{"ping": {Method: http.MethodGet, Pattern: "/ping"}},
			principal: nil,
			want:      "a principal is required",
		},
	}

	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			principal := tc.principal
			if principal == nil && label != "a missing principal" {
				principal = httpx.Anonymous
			}
			mux := http.NewServeMux()

			err := httpx.Mount(mux, newRegistry(), tc.routes, principal)

			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
			// Nothing may be mounted once any check has failed.
			for _, target := range []string{"/ping", "/shared", "/authors", "/a/b/c"} {
				if rec := serve(mux, http.MethodGet, target, nil); rec.Code != http.StatusNotFound {
					t.Errorf("a route was mounted anyway: %s answered %d", target, rec.Code)
				}
			}
		})
	}
}

// A route table is usually wrong in more than one way at once, and fixing it one
// restart at a time is the slowest possible loop.
func TestMountReportsEveryProblemAtOnce(t *testing.T) {
	err := httpx.Mount(http.NewServeMux(), newRegistry(), map[string]httpx.Route{
		"create_author": {Method: http.MethodGet, Pattern: "/authors"},
		"get_author":    {Method: http.MethodGet, Pattern: "/authors/{slug}"},
		"nope":          {Method: http.MethodGet, Pattern: "/nope"},
	}, httpx.Anonymous)

	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"create_author", "get_author", "nope"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
	// Ordered by spec name, so the same table produces the same report each run.
	report := err.Error()
	create := strings.Index(report, "create_author")
	get := strings.Index(report, "get_author")
	nope := strings.Index(report, `"nope"`)
	if create >= get || get >= nope {
		t.Errorf("problems are not ordered by spec name:\n%v", report)
	}
}

// The methods each Kind accepts, as this adapter mounts them. The rule is the
// kernel's; what is checked here is that every arm of it is reachable through
// Mount.
func TestMountAcceptsEveryMethodItsKindAllows(t *testing.T) {
	cases := map[string]struct {
		spec   string
		method string
	}{
		"a query on GET":       {spec: "list_authors", method: http.MethodGet},
		"a query on HEAD":      {spec: "list_authors", method: http.MethodHead},
		"a query on OPTIONS":   {spec: "list_authors", method: http.MethodOptions},
		"a mutation on POST":   {spec: "create_author", method: http.MethodPost},
		"a mutation on PUT":    {spec: "create_author", method: http.MethodPut},
		"a mutation on PATCH":  {spec: "create_author", method: http.MethodPatch},
		"a mutation on DELETE": {spec: "delete_author", method: http.MethodDelete},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			err := httpx.Mount(http.NewServeMux(), newRegistry(), map[string]httpx.Route{
				tc.spec: {Method: tc.method, Pattern: "/thing"},
			}, httpx.Anonymous)
			if err != nil {
				t.Errorf("Mount: %v", err)
			}
		})
	}
}

// A mount with no routes is a mount that does nothing, which is never what
// someone meant to write.
func TestMountRefusesAnEmptyRouteTable(t *testing.T) {
	for label, routes := range map[string]map[string]httpx.Route{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(label, func(t *testing.T) {
			err := httpx.Mount(http.NewServeMux(), newRegistry(), routes, httpx.Anonymous)
			if err == nil {
				t.Fatal("want an error for a mount that names no routes")
			}
		})
	}
}

// The one conflict Mount cannot prove in advance: a pattern the caller had
// already put on the mux itself.
func TestMountReportsAConflictWithTheCallersOwnRoutes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authors/{id}", func(http.ResponseWriter, *http.Request) {})

	err := httpx.Mount(mux, newRegistry(), map[string]httpx.Route{
		"get_author": {Method: http.MethodGet, Pattern: "/authors/{id}"},
	}, httpx.Anonymous)

	if err == nil {
		t.Fatal("want an error for a pattern the caller had already registered")
	}
	if !strings.Contains(err.Error(), "/authors/{id}") {
		t.Errorf("error = %v, want it to name the pattern", err)
	}
}

// A route whose status the Handler primitive would refuse must be refused by
// Mount too, whatever order the checks happen to run in.
func TestMountRejectsAStatusHandlerWouldRefuse(t *testing.T) {
	err := httpx.Mount(http.NewServeMux(), newRegistry(), map[string]httpx.Route{
		"ping": {Method: http.MethodGet, Pattern: "/ping"},
	}, httpx.Anonymous, httpx.WithStatus(1))

	if err == nil {
		t.Fatal("want an error for a mount-wide status that cannot be sent")
	}
}

// The seam between the two halves of the capture check: this adapter extracts
// the names, the kernel decides whether the input can receive them. A
// multi-segment wildcard is declared "{tags...}" and has to be handed over as
// "tags", or every one of them would be reported as undeclared.
func TestMountBindsAMultiSegmentCapture(t *testing.T) {
	mux := mustMount(t, map[string]httpx.Route{
		"list_authors": {Method: http.MethodGet, Pattern: "/files/{tags...}"},
	}, httpx.Anonymous)

	rec := serve(mux, http.MethodGet, "/files/a/b", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"a/b"`) {
		t.Errorf("body = %s, want the captured path bound to tags", rec.Body)
	}
}
