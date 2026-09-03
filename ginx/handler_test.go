package ginx_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Artui/go-services"
	"github.com/Artui/go-services/ginx"
	"github.com/gin-gonic/gin"
)

// authorRoutes is the table most of the request-path tests serve.
func authorRoutes() map[string]ginx.Route {
	return map[string]ginx.Route{
		"get_author":    {Method: "GET", Path: "/authors/:id"},
		"list_authors":  {Method: "GET", Path: "/authors"},
		"create_author": {Method: "POST", Path: "/authors"},
		"retire_author": {Method: "DELETE", Path: "/authors/:id"},
		"note_scope":    {Method: "PUT", Path: "/tenants/:tenant/notes"},
		"refuse":        {Method: "GET", Path: "/refuse/:id"},
		"clash":         {Method: "POST", Path: "/clash"},
		"boom":          {Method: "GET", Path: "/boom/:id"},
		"vague":         {Method: "GET", Path: "/vague/:id"},
	}
}

func TestSuccess(t *testing.T) {
	e := engineFor(t, authorRoutes())

	for _, tc := range []struct {
		name, method, target, body string
		status                     int
		want                       string
	}{
		{
			name:   "path capture coerces to the schema's type",
			method: http.MethodGet, target: "/authors/1",
			status: http.StatusOK, want: `{"id":1,"name":"ada"}`,
		},
		{
			name:   "the spec's own status is used",
			method: http.MethodPost, target: "/authors", body: `{"name":"grace"}`,
			status: http.StatusCreated, want: `{"name":"grace","by":"ada"}`,
		},
		{
			name:   "query string coerces per property, including a repeated key",
			method: http.MethodGet, target: "/authors?limit=5&tags=a&tags=b",
			status: http.StatusOK, want: `{"limit":5,"tags":["a","b"]}`,
		},
		{
			name:   "a parameter the schema does not declare is dropped, not rejected",
			method: http.MethodGet, target: "/authors?utm_source=newsletter",
			status: http.StatusOK, want: `{}`,
		},
		{
			name:   "no body and no parameters is an empty payload, not a malformed one",
			method: http.MethodGet, target: "/authors",
			status: http.StatusOK, want: `{}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertJSON(t, do(e, tc.method, tc.target, bodyOf(tc.body)), tc.status, tc.want)
		})
	}
}

// A 204 must not carry a body. Gin's own renderer drops it for a status that
// forbids one, which is worth a test because the equivalent net/http code has
// to do it by hand.
func TestNoContentCarriesNoBody(t *testing.T) {
	rec := do(engineFor(t, authorRoutes()), http.MethodDelete, "/authors/1", nil)
	assertJSON(t, rec, http.StatusNoContent, "")
}

// The precedence rule is the security-relevant one: a route capture scopes the
// operation, and neither a query parameter nor a body field may take that back.
func TestParamsPrecedence(t *testing.T) {
	e := engineFor(t, authorRoutes())
	rec := do(e, http.MethodPut, "/tenants/acme/notes?tenant=elsewhere",
		strings.NewReader(`{"tenant":"from-body","note":"hello"}`))
	assertJSON(t, rec, http.StatusOK, `{"tenant":"acme","note":"hello"}`)
}

// The second half of the same rule, on a route with no capture at all, so that
// query-beats-body is tested on its own rather than inferred from the case
// where a capture beats both.
func TestQueryBeatsBody(t *testing.T) {
	e := gin.New()
	// Handler rather than Mount: one spec can only appear once in a route
	// table, and this route is a second placement of note_scope.
	h, err := ginx.Handler(newRegistry(t), "note_scope", staticPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	e.PUT("/notes", h)

	rec := do(e, http.MethodPut, "/notes?tenant=from-query",
		strings.NewReader(`{"tenant":"from-body","note":"hello"}`))
	assertJSON(t, rec, http.StatusOK, `{"tenant":"from-query","note":"hello"}`)
}

func TestErrorMapping(t *testing.T) {
	e := engineFor(t, authorRoutes())

	for _, tc := range []struct {
		name, method, target, body string
		status                     int
		want                       string
	}{
		{
			name:   "a validation layer's per-field message keeps its field",
			method: http.MethodPost, target: "/authors", body: `{"name":"   "}`,
			status: http.StatusBadRequest, want: `{"errors":{"name":["must not be blank"]}}`,
		},
		{
			name:   "a value the schema cannot coerce is attributed to its parameter",
			method: http.MethodGet, target: "/authors?limit=lots",
			status: http.StatusBadRequest,
			want:   `{"errors":{"limit":["expected an integer, got \"lots\""]}}`,
		},
		{
			name:   "a wrapped ErrPermission keeps the service's own words",
			method: http.MethodGet, target: "/refuse/1",
			status: http.StatusForbidden, want: `{"error":"services: permission denied: not yours"}`,
		},
		{
			name:   "a wrapped ErrNotFound keeps the service's own words",
			method: http.MethodGet, target: "/authors/2",
			status: http.StatusNotFound, want: `{"error":"services: not found: author 2"}`,
		},
		{
			name:   "a wrapped ErrConflict keeps the service's own words",
			method: http.MethodPost, target: "/clash", body: `{"id":1}`,
			status: http.StatusConflict, want: `{"error":"services: conflict: already retired"}`,
		},
		{
			name:   "an unmapped error says one fixed sentence",
			method: http.MethodGet, target: "/boom/1",
			status: http.StatusInternalServerError, want: `{"error":"internal server error"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertJSON(t, do(e, tc.method, tc.target, bodyOf(tc.body)), tc.status, tc.want)
		})
	}
}

// A body the request could not use has to read the same whether EncodeParams
// or Dispatch is the one that noticed, because which of them notices depends
// only on whether the route happened to capture a segment -- a distinction the
// client cannot see and did not cause.
func TestBodyProblemsReadTheSameOnBothPaths(t *testing.T) {
	// The same spec on two routes, so a difference in the answer is the two
	// code paths disagreeing rather than two schemas disagreeing. Handler for
	// both, because a route table holds one route per spec.
	e := gin.New()
	h, err := ginx.Handler(newRegistry(t), "note_scope", staticPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	// No capture and no query string, so EncodeParams hands the body straight
	// through and Dispatch is what finds it.
	e.PUT("/notes", h)
	// A capture, so EncodeParams has to parse the body itself to fold it in.
	e.PUT("/tenants/:tenant/notes", h)

	for _, tc := range []struct{ name, body, want string }{
		{
			name: "a body that is not JSON at all",
			body: `{`,
			want: `{"errors":{"non_field_errors":` +
				`["malformed JSON body: unexpected end of JSON input"]}}`,
		},
		{
			// Valid JSON of the wrong shape. It is not malformed, so it travels
			// on for the schema to reject accurately rather than being called
			// malformed by one path and a type error by the other.
			name: "a body that is JSON but not an object",
			body: `[1,2]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			byKernel := do(e, http.MethodPut, "/notes", strings.NewReader(tc.body))
			byEncodeParams := do(e, http.MethodPut, "/tenants/acme/notes",
				strings.NewReader(tc.body))

			for _, rec := range []*httptest.ResponseRecorder{byKernel, byEncodeParams} {
				if rec.Code != http.StatusBadRequest {
					t.Errorf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
				}
			}
			if byKernel.Body.String() != byEncodeParams.Body.String() {
				t.Errorf("the two paths disagree:\n  %s\n  %s", byKernel.Body, byEncodeParams.Body)
			}
			if tc.want != "" {
				assertJSON(t, byKernel, http.StatusBadRequest, tc.want)
			}
		})
	}
}

// Dispatch refuses an undeclared capture too, and Mount cannot cover every
// case: a handler placed on somebody else's router has no pattern for Mount to
// inspect. This is that backstop.
//
// It is a 500, not a 400. The message is addressed to whoever wrote the route
// and no change the caller could make would help, so telling them to fix their
// request would be a lie -- and the operator, who can fix it, is the one who
// gets the words.
func TestUndeclaredCaptureIsRefusedAtRequestTime(t *testing.T) {
	var observed error
	e := gin.New()
	// list_authors declares limit and tags, and nothing called tenant.
	h, err := ginx.Handler(newRegistry(t), "list_authors", staticPrincipal,
		ginx.WithErrorHandler(func(_ *gin.Context, err error) { observed = err }))
	if err != nil {
		t.Fatal(err)
	}
	// Placed by hand rather than mounted, which is the case Mount cannot see.
	e.GET("/tenants/:tenant/authors", h)

	rec := do(e, http.MethodGet, "/tenants/acme/authors", nil)
	assertJSON(t, rec, http.StatusInternalServerError, `{"error":"internal server error"}`)

	// The client is told nothing and the operator is told everything, which is
	// the whole point of answering a configuration fault with a 500.
	if observed == nil || !errors.Is(observed, services.ErrConfiguration) {
		t.Fatalf("WithErrorHandler saw %v, want a configuration error", observed)
	}
	if !strings.Contains(observed.Error(), "tenant") {
		t.Errorf("error = %q, want the capture named", observed)
	}
}

// A validation failure with no fields must still be an object, or a client
// parsing "errors" as one cannot read the response at all.
func TestValidationFailureWithNoFields(t *testing.T) {
	rec := do(engineFor(t, authorRoutes()), http.MethodGet, "/vague/1", nil)
	assertJSON(t, rec, http.StatusBadRequest, `{"errors":{}}`)
}

// An unbounded read is a denial of service waiting to happen, so the body is
// bounded before the first byte of it is allocated. The ceiling is the kernel's
// constant rather than an option here: two transports refusing at different
// sizes is a difference no client can predict.
func TestBodyTooLarge(t *testing.T) {
	oversized := `{"name":"` + strings.Repeat("a", int(services.DefaultMaxBodyBytes)) + `"}`
	rec := do(engineFor(t, authorRoutes()), http.MethodPost, "/authors",
		strings.NewReader(oversized))
	assertJSON(t, rec, http.StatusRequestEntityTooLarge, `{"error":"request body too large"}`)
}

// failingBody stands in for a client that hung up mid-upload.
type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("connection reset by peer") }

func TestBodyThatCannotBeRead(t *testing.T) {
	rec := do(engineFor(t, authorRoutes()), http.MethodPost, "/authors", failingBody{})
	assertJSON(t, rec, http.StatusBadRequest,
		`{"errors":{"non_field_errors":["the request body could not be read"]}}`)
}

// An unexpected error must reach whoever operates the service and nobody else.
func TestUnmappedErrorIsReportedButNotServed(t *testing.T) {
	var observed error
	var collected []string

	e := gin.New()
	// Registered before the route, so it wraps it: Gin composes the chain at
	// registration time.
	e.Use(func(c *gin.Context) {
		c.Next()
		for _, err := range c.Errors {
			collected = append(collected, err.Error())
		}
	})
	err := ginx.Mount(e, newRegistry(t), map[string]ginx.Route{
		"boom":       {Method: "GET", Path: "/boom/:id"},
		"get_author": {Method: "GET", Path: "/authors/:id"},
	}, staticPrincipal, ginx.WithErrorHandler(func(_ *gin.Context, err error) { observed = err }))
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	rec := do(e, http.MethodGet, "/boom/1", nil)
	assertJSON(t, rec, http.StatusInternalServerError, `{"error":"internal server error"}`)
	if strings.Contains(rec.Body.String(), operatorText) {
		t.Errorf("the response repeated the operator's error: %s", rec.Body.String())
	}
	if observed == nil || !strings.Contains(observed.Error(), operatorText) {
		t.Errorf("WithErrorHandler saw %v, want the real error", observed)
	}
	if len(collected) != 1 || !strings.Contains(collected[0], operatorText) {
		t.Errorf("c.Errors = %v, want the real error", collected)
	}

	// A mapped refusal already told the client what happened, so it goes to
	// Gin's error channel but not to the callback, which exists for the case
	// the client was told nothing.
	observed, collected = nil, nil
	assertJSON(t, do(e, http.MethodGet, "/authors/2", nil), http.StatusNotFound,
		`{"error":"services: not found: author 2"}`)
	if observed != nil {
		t.Errorf("WithErrorHandler saw %v, want nothing for a mapped error", observed)
	}
	if len(collected) != 1 {
		t.Errorf("c.Errors = %v, want the 404 pushed onto Gin's channel", collected)
	}
}

// A handler that refused must stop the chain, or a later handler writes a
// second response onto a request this one already answered.
func TestFailureAbortsTheChain(t *testing.T) {
	reached := false
	e := gin.New()
	h, err := ginx.Handler(newRegistry(t), "boom", staticPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	e.GET("/boom/:id", h, func(*gin.Context) { reached = true })

	do(e, http.MethodGet, "/boom/1", nil)
	if reached {
		t.Error("the handler after a refusal ran")
	}
}

func TestPrincipalFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{
			name:   "a refusal is the principal function's to make",
			err:    fmt.Errorf("%w: token expired", services.ErrPermission),
			status: http.StatusForbidden,
			want:   `{"error":"services: permission denied: token expired"}`,
		},
		{
			// Failing to authenticate is not the same as being refused: an
			// unreachable identity provider is the operator's problem.
			name:   "anything else is the operator's problem",
			err:    errors.New(operatorText),
			status: http.StatusInternalServerError,
			want:   `{"error":"internal server error"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := gin.New()
			h, err := ginx.Handler(newRegistry(t), "get_author",
				func(*gin.Context) (any, error) { return nil, tc.err })
			if err != nil {
				t.Fatal(err)
			}
			e.GET("/authors/:id", h)
			assertJSON(t, do(e, http.MethodGet, "/authors/1", nil), tc.status, tc.want)
		})
	}
}

// Anonymous is what a public mount says out loud. The spec echoes the principal
// back through the registry's resolver, so an empty "by" is proof that nil
// travelled the whole way rather than being replaced somewhere.
func TestAnonymous(t *testing.T) {
	e := gin.New()
	err := ginx.Mount(e, newRegistry(t),
		map[string]ginx.Route{"create_author": {Method: "POST", Path: "/authors"}}, ginx.Anonymous)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	rec := do(e, http.MethodPost, "/authors", strings.NewReader(`{"name":"grace"}`))
	assertJSON(t, rec, http.StatusCreated, `{"name":"grace","by":""}`)
}

func TestHandlerRefusesBadConfiguration(t *testing.T) {
	reg := newRegistry(t)

	for _, tc := range []struct {
		name string
		call func() (gin.HandlerFunc, error)
		want string
	}{
		{
			name: "a nil registry",
			call: func() (gin.HandlerFunc, error) {
				return ginx.Handler[deps](nil, "get_author", staticPrincipal)
			},
			want: "needs a registry",
		},
		{
			name: "an unregistered name",
			call: func() (gin.HandlerFunc, error) {
				return ginx.Handler(reg, "no_such_spec", staticPrincipal)
			},
			want: `no spec named "no_such_spec"`,
		},
		{
			name: "a missing principal function",
			call: func() (gin.HandlerFunc, error) {
				return ginx.Handler(reg, "get_author", nil)
			},
			want: "ginx.Anonymous",
		},
		{
			name: "a status no response can carry",
			call: func() (gin.HandlerFunc, error) {
				return ginx.Handler(reg, "get_author", staticPrincipal, ginx.WithStatus(42))
			},
			want: "42 is not an HTTP status code",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := tc.call()
			if err == nil {
				t.Fatal("got no error, want a refusal")
			}
			if h != nil {
				t.Error("a refused Handler still returned a handler")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestWithStatusOverridesTheSpec(t *testing.T) {
	e := gin.New()
	h, err := ginx.Handler(newRegistry(t), "create_author", staticPrincipal,
		ginx.WithStatus(http.StatusAccepted))
	if err != nil {
		t.Fatal(err)
	}
	e.POST("/authors", h)

	rec := do(e, http.MethodPost, "/authors", strings.NewReader(`{"name":"grace"}`))
	assertJSON(t, rec, http.StatusAccepted, `{"name":"grace","by":"ada"}`)
}
