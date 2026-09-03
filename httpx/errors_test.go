package httpx_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/httpx"
)

// The mapping table, end to end. Each spec wraps its sentinel with fmt.Errorf
// rather than returning it bare, so a pass here is evidence the adapter matches
// with errors.Is and not on identity.
func TestErrorMapping(t *testing.T) {
	cases := map[string]struct {
		spec       string
		wantStatus int
		wantText   string
	}{
		"not found":     {spec: "gone", wantStatus: http.StatusNotFound, wantText: "author 42"},
		"conflict":      {spec: "taken", wantStatus: http.StatusConflict, wantText: "slug already used"},
		"permission":    {spec: "refused", wantStatus: http.StatusForbidden, wantText: "not an editor"},
		"anything else": {spec: "exploded", wantStatus: http.StatusInternalServerError, wantText: "internal server error"},
		"cannot be encoded": {
			spec: "unencodable", wantStatus: http.StatusInternalServerError, wantText: "internal server error",
		},
	}

	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			h := mustHandler(t, tc.spec, httpx.Anonymous)

			rec := serve(h, http.MethodGet, "/", nil)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body)
			}
			var body struct {
				Error string `json:"error"`
			}
			decode(t, rec, &body)
			if !strings.Contains(body.Error, tc.wantText) {
				t.Errorf("error = %q, want it to contain %q", body.Error, tc.wantText)
			}
		})
	}
}

// The rule the 500 case exists for, stated on its own so it cannot be lost in a
// table: an unexpected error's words go to the observer and never to the client.
func TestUnexpectedErrorIsRedactedAndObserved(t *testing.T) {
	var (
		gotStatus int
		gotErr    error
		gotPath   string
	)
	h := mustHandler(t, "exploded", httpx.Anonymous, httpx.WithOnError(
		func(r *http.Request, status int, err error) {
			gotStatus, gotErr, gotPath = status, err, r.URL.Path
		},
	))

	rec := serve(h, http.MethodGet, "/boom", nil)

	if got := rec.Body.String(); strings.Contains(got, "10.0.0.7") {
		t.Fatalf("body %s leaks the operator's error text", got)
	}
	if gotStatus != http.StatusInternalServerError {
		t.Errorf("observed status = %d, want 500", gotStatus)
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "10.0.0.7:5432") {
		t.Errorf("observed error = %v, want the real one", gotErr)
	}
	if gotPath != "/boom" {
		t.Errorf("observed path = %q, want the request", gotPath)
	}
}

// A value the encoder cannot represent is discovered after the spec has already
// succeeded. It must still become a 500 rather than a 200 truncated mid-body.
func TestUnencodableValueIsAWholeFiveHundred(t *testing.T) {
	var observed []error
	h := mustHandler(t, "unencodable", httpx.Anonymous, httpx.WithOnError(
		func(_ *http.Request, _ int, err error) { observed = append(observed, err) },
	))

	rec := serve(h, http.MethodGet, "/", nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if got := rec.Body.String(); got != `{"error":"internal server error"}` {
		t.Errorf("body = %s, want the whole fixed sentence", got)
	}
	if len(observed) != 1 {
		t.Fatalf("observed %d errors, want 1", len(observed))
	}
	if !strings.Contains(observed[0].Error(), "NaN") {
		t.Errorf("observed = %v, want the encoder's own complaint", observed[0])
	}
}

func TestValidationFailureCarriesItsFields(t *testing.T) {
	h := mustHandler(t, "create_author", httpx.Anonymous)

	rec := serve(h, http.MethodPost, "/", strings.NewReader(`{"id":1,"name":"  "}`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	var body struct {
		Errors map[string][]string `json:"errors"`
	}
	decode(t, rec, &body)
	if msgs := body.Errors["name"]; len(msgs) != 1 || msgs[0] != "must not be blank" {
		t.Errorf("errors = %v, want the Validate message under its JSON field name", body.Errors)
	}
}

func TestSchemaFailureIsAWholePayloadMessage(t *testing.T) {
	h := mustHandler(t, "get_author", httpx.Anonymous)

	// id is required and absent: the schema layer reports a path rather than a
	// field, so the kernel files it under its whole-payload key and so does the
	// wire.
	rec := serve(h, http.MethodGet, "/", nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var body struct {
		Errors map[string][]string `json:"errors"`
	}
	decode(t, rec, &body)
	if len(body.Errors[services.NonFieldKey]) == 0 {
		t.Errorf("errors = %v, want a message under %q", body.Errors, services.NonFieldKey)
	}
}

func TestMalformedBodyIsAFourHundred(t *testing.T) {
	h := mustHandler(t, "create_author", httpx.Anonymous)

	rec := serve(h, http.MethodPost, "/?id=1", strings.NewReader(`{not json`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestUncoercibleParameterIsAFourHundred(t *testing.T) {
	h := mustHandler(t, "get_author", httpx.Anonymous)

	rec := serve(h, http.MethodGet, "/?id=not-a-number", nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	var body struct {
		Errors map[string][]string `json:"errors"`
	}
	decode(t, rec, &body)
	if len(body.Errors["id"]) == 0 {
		t.Errorf("errors = %v, want the failure filed under the parameter", body.Errors)
	}
}

// A ValidationError with no fields at all is constructible, and rendering its
// nil map would put {"errors": null} on the wire -- which a client parsing
// errors as an object cannot read. The kernel's FieldMap is what keeps the
// shape, so both HTTP adapters answer the same one.
func TestFieldlessValidationErrorStillRendersAnObject(t *testing.T) {
	h := mustHandler(t, "fieldless", httpx.Anonymous)

	rec := serve(h, http.MethodGet, "/", nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := rec.Body.String(); got != `{"errors":{}}` {
		t.Errorf("body = %s, want an empty object rather than null", got)
	}
}

// The Principal function's own failures go through the same table. That is the
// distinction a middleware chain which can only "return 401" throws away: a
// rejected token and an unreachable auth backend are not the same incident.
func TestPrincipalFailuresAreMappedLikeAnyOther(t *testing.T) {
	cases := map[string]struct {
		err  error
		want int
	}{
		"a rejected token":             {err: services.ErrPermission, want: http.StatusForbidden},
		"an auth backend that is down": {err: errUnreachable, want: http.StatusInternalServerError},
	}

	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			h := mustHandler(t, "ping", func(*http.Request) (any, error) { return nil, tc.err })

			rec := serve(h, http.MethodGet, "/", nil)

			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

// errUnreachable stands in for an auth dependency that is down rather than for
// a caller who was refused.
var errUnreachable = errors.New("session store unreachable")

// A principal the resolver does not recognise is refused by the kernel rather
// than by this adapter, and still arrives as a 403.
func TestUnrecognisedPrincipalIsRefusedByTheResolver(t *testing.T) {
	h := mustHandler(t, "ping", func(*http.Request) (any, error) { return 42, nil })

	rec := serve(h, http.MethodGet, "/", nil)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body)
	}
}

// The case Mount could never have caught, and the reason the dispatch-time
// half of the capture check has to exist: a Handler behind a router with its
// own capture syntax has no ServeMux pattern for anything to inspect, so the
// only layer that can see an undeclared capture is the one that binds it.
//
// It answers 500, not 400. The route table is what is wrong, no change the
// caller could make would help, and the diagnostic that says so is addressed to
// whoever wrote it -- so it goes to the observer while the client gets the
// same fixed sentence as any other fault on this side of the line.
func TestAnUndeclaredCaptureFromAForeignRouterIsRefused(t *testing.T) {
	var observed []error
	var observedStatus int
	h := mustHandler(t, "get_author", httpx.Anonymous,
		httpx.WithPathValues(func(*http.Request) map[string][]string {
			// What "/tenants/{tenant}/authors/{id}" would deliver from a router
			// this adapter knows nothing about. get_author declares no tenant.
			return map[string][]string{"tenant": {"acme"}, "id": {"7"}}
		}),
		httpx.WithOnError(func(_ *http.Request, status int, err error) {
			observedStatus = status
			observed = append(observed, err)
		}),
	)

	rec := serve(h, http.MethodGet, "/", nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body)
	}
	if want := `{"error":"` + services.InternalErrorText + `"}`; rec.Body.String() != want {
		t.Errorf("body = %s, want %s", rec.Body, want)
	}
	// The scope must not have quietly become a successful, unscoped call. This
	// is the assertion that would have failed before the kernel refused an
	// undeclared capture: the operation ran, and answered 200.
	if strings.Contains(rec.Body.String(), `"viewer"`) {
		t.Errorf("the operation ran anyway: %s", rec.Body)
	}
	// The half that redaction is only affordable because of.
	if len(observed) != 1 {
		t.Fatalf("observed %d errors, want 1", len(observed))
	}
	if observedStatus != http.StatusInternalServerError {
		t.Errorf("observed status = %d, want 500", observedStatus)
	}
	if !errors.Is(observed[0], services.ErrConfiguration) {
		t.Errorf("observed = %v, want a configuration error rather than the caller's", observed[0])
	}
	if !strings.Contains(observed[0].Error(), "tenant") {
		t.Errorf("observed = %v, want it to name the capture", observed[0])
	}
}

// A body that is valid JSON but not an object is not malformed, and is no
// longer described as though it were. It goes to the schema layer, which can say
// what is actually wrong with it.
func TestAWellFormedNonObjectBodyIsNotCalledMalformed(t *testing.T) {
	h := mustHandler(t, "create_author", httpx.Anonymous)

	rec := serve(h, http.MethodPost, "/?id=1", strings.NewReader(`[1,2]`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "malformed") {
		t.Errorf("body = %s, want the schema's complaint rather than a syntax one", rec.Body)
	}
}
