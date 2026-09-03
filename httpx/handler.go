package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	services "github.com/Artui/go-services"
)

// Principal authenticates a request into the opaque value the kernel hands to
// the registry's resolver.
//
// It is this adapter's entire share of identity: read a header, a cookie or a
// session and return something the resolver can assert its own type on. The
// kernel deliberately keeps the type opaque here so that the one place an
// application names its own user type is its resolver -- see services.New.
//
// Returning an error refuses the request through the same mapping as any other
// failure, which is worth more than it looks: a rejected token is
// services.ErrPermission and so a 403, while an auth backend that is down is
// any other error and so a 500. That is the distinction an on-call engineer
// needs at three in the morning, and the one a middleware chain that can only
// "return 401" throws away.
type Principal func(*http.Request) (any, error)

// Anonymous is the Principal for a mount that authenticates nobody. It
// dispatches a nil principal, which the registry's resolver is free to accept
// or to refuse.
//
// It exists so that a public mount has to say so. A nil Principal is refused at
// construction instead of being read as "no authentication", because those two
// look identical in a call and only one of them was meant: a mount that forgot
// its Principal and a mount that has none must not be the same line of code.
func Anonymous(*http.Request) (any, error) { return nil, nil }

// Handler serves one registered spec over HTTP.
//
// It is the primitive; Mount is a loop over it plus the configuration checks
// that need a route to check against. Reach for it directly when the router is
// not a ServeMux, when one spec has to answer on several routes, or when a
// route captures a segment the input deliberately has no property for.
//
// It returns an error rather than a handler that fails at request time, because
// an unregistered name, a missing Principal and an unsendable status are all
// configuration bugs, and this library's position is that a configuration bug
// belongs to start-up.
//
// Note what is NOT checked here: nothing stops a Query being served on POST,
// because a bare Handler has no route to read a method off. Mount is where the
// method rule is applied, using the kernel's own Kind.AllowsMethod.
func Handler[D any](
	reg *services.Registry[D], name string, principal Principal, opts ...Option,
) (http.Handler, error) {
	entry, ok := reg.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("httpx: no spec named %q is registered", name)
	}
	if principal == nil {
		return nil, fmt.Errorf(
			"httpx: %q: a principal is required; pass httpx.Anonymous to mount it unauthenticated",
			name,
		)
	}
	cfg := newConfig(opts)
	if cfg.status != 0 && !validStatus(cfg.status) {
		return nil, fmt.Errorf("httpx: %q: %d cannot be sent as a response status", name, cfg.status)
	}
	return &handler[D]{reg: reg, entry: entry, principal: principal, cfg: cfg}, nil
}

// handler is one mounted spec. Every field is set at construction and read-only
// afterwards, so one handler serves every request for its route concurrently
// with no state of its own.
type handler[D any] struct {
	reg       *services.Registry[D]
	entry     services.Entry
	principal Principal
	cfg       config
}

// ServeHTTP is the whole of the adapter's request path, and it is short on
// purpose: authenticate, assemble a JSON payload, dispatch, render. Validation,
// permission checks, the transaction boundary and the error taxonomy are all
// below Dispatch, where a second adapter cannot forget them.
func (h *handler[D]) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	principal, err := h.principal(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	raw, err := h.payload(w, r)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	result, err := h.reg.Dispatch(r.Context(), principal, h.entry.Name, raw)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// Result.Status is the spec's own hint and is never zero -- the kernel
	// defaults it at registration -- so the only question is whether this
	// mount overrode it.
	status := h.cfg.status
	if status == 0 {
		status = result.Status
	}
	h.write(w, r, status, result.Value)
}

// payload assembles the JSON document the kernel dispatches on.
//
// The two parameter sources are handed over separately and unmerged, which is
// the point: precedence is body, then query, then captures, and the kernel
// enforces it rather than describing it. An adapter that merged them the other
// way would let a client rescope a path-scoped operation --
// "/tenants/{tenant}/reports" answered with "?tenant=other" -- and nothing
// would fail while it did.
//
// The coercion is the kernel's for the same reason. A query string is all
// strings and only the schema knows that "?limit=10" means 10 rather than "10";
// two HTTP adapters guessing at that separately would eventually disagree about
// the same spec.
func (h *handler[D]) payload(w http.ResponseWriter, r *http.Request) (json.RawMessage, error) {
	body, err := h.body(w, r)
	if err != nil {
		return nil, err
	}
	return services.EncodeParams(h.entry.Input, body, r.URL.Query(), h.cfg.pathValues(r))
}

// body reads the request body, bounded by services.DefaultMaxBodyBytes.
//
// It reads whatever the method is, rather than consulting a table of which
// methods carry a body: a GET arrives with an empty one, EncodeParams passes an
// empty body straight through, and Dispatch reads an empty payload as an empty
// object. The method question that does matter -- a Query on POST, a Mutation
// on GET -- is settled at Mount, before any request exists.
func (h *handler[D]) body(w http.ResponseWriter, r *http.Request) (json.RawMessage, error) {
	// MaxBytesReader, not io.LimitReader: the difference is that this one tells
	// the server to close the connection rather than silently truncating the
	// payload into something that might still parse into a valid-looking
	// operation.
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, services.DefaultMaxBodyBytes))
	if err == nil {
		return raw, nil
	}

	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		// The limit is carried on the error for the observer's benefit. The
		// client is told only that it sent too much: the number is this
		// deployment's business, and knowing it exactly helps nobody who is not
		// probing.
		return nil, fmt.Errorf("%w: over %d bytes", services.ErrBodyTooLarge, tooLarge.Limit)
	}
	// Anything else is a transport failure -- a client that hung up part-way, a
	// connection reset. net/http's own text describes the connection rather
	// than anything the client can act on, so the wire gets the kernel's
	// sentence and the observer gets the rest.
	return nil, &services.ValidationError{
		Fields: map[string][]string{services.NonFieldKey: {services.UnreadableBodyText}},
	}
}

// fail renders err and reports it to the observer.
//
// The observer is called for every failed request rather than only the redacted
// one, because it is given the status too: a caller wanting just the 500s
// writes one comparison, whereas a caller wanting the 403s out of a hook that
// only fired on 500 has nowhere to go.
func (h *handler[D]) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, body := errorResponseFor(err)
	h.observe(r, status, err)
	h.write(w, r, status, body)
}

// write renders v as the response body at status.
//
// The value is marshalled into memory before anything is written. Encoding
// straight into the ResponseWriter commits a 200 and then fails part-way
// through the body, handing the client a truncated success it has no way to
// detect; buffering costs one copy and buys the ability to still send a 500.
func (h *handler[D]) write(w http.ResponseWriter, r *http.Request, status int, v any) {
	if !bodyAllowedForStatus(status) {
		w.WriteHeader(status)
		return
	}

	body, err := json.Marshal(v)
	if err != nil {
		// The service returned something no encoder can represent: a NaN, a
		// channel, a cycle. That is this process's bug rather than the
		// client's, so it is redacted and reported like any other one.
		h.observe(r, http.StatusInternalServerError, err)
		status, body = http.StatusInternalServerError, internalBody
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status line is already on the wire, so a write failure -- a client
	// that hung up mid-response -- has nobody left to tell.
	_, _ = w.Write(body)
}

func (h *handler[D]) observe(r *http.Request, status int, err error) {
	if h.cfg.onError != nil {
		h.cfg.onError(r, status, err)
	}
}
