package ginx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Artui/go-services"
	"github.com/gin-gonic/gin"
)

// jsonContentType is what Gin's own JSON renderer sets, repeated here because
// this package encodes the body itself. Keeping the two in step matters: a
// consumer switching between c.JSON and this must not see the header change.
const jsonContentType = "application/json; charset=utf-8"

// unwrap reaches the http.ResponseWriter the server actually handed Gin.
//
// net/http's MaxBytesReader tells the connection not to bother draining an
// oversized body, and it does so through an unexported method on the server's
// own response value. gin.ResponseWriter embeds http.ResponseWriter as an
// interface, so that method is not promoted through it and the hint is
// silently dropped. Unwrap is the convention net/http itself uses for exactly
// this case (see http.ResponseController), and Gin implements it.
func unwrap(w http.ResponseWriter) http.ResponseWriter {
	for {
		inner, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return w
		}
		w = inner.Unwrap()
	}
}

// PrincipalFunc authenticates a request and returns the opaque principal the
// kernel hands to the registry's resolver.
//
// It returns any rather than a typed identity on purpose: the resolver passed
// to services.New is the one place an application asserts its own identity
// type, so this package never learns what a user is. Returning an error that
// wraps services.ErrPermission answers 403; any other error answers 500, on the
// grounds that failing to authenticate is not the same as being refused.
type PrincipalFunc func(*gin.Context) (any, error)

// Anonymous is the PrincipalFunc for a mount that authenticates nobody.
//
// It exists so that "this API is public" is written down rather than inferred
// from a nil. Handler and Mount refuse a nil principal function, because the
// difference between an unauthenticated mount and one whose authentication was
// forgotten is not visible in any test that only checks status codes.
func Anonymous(*gin.Context) (any, error) { return nil, nil }

// Handler builds the gin.HandlerFunc that serves one spec.
//
// It is the primitive; Mount is the convenience built on it. Reach for it
// directly to place a handler yourself -- inside a group with its own
// middleware, on a route pattern the table cannot express, or on two paths for
// the same spec.
//
// One rule Mount enforces is not available here: Handler cannot see the method
// it will be registered on, so the Kind-versus-method check (a query on POST, a
// mutation on GET) belongs to Mount. A caller placing handlers by hand is
// choosing to make that check themselves.
//
// It returns an error rather than panicking, and every error it returns is a
// configuration mistake that would otherwise have waited for a request.
func Handler[D any](
	reg *services.Registry[D], name string, principal PrincipalFunc, opts ...Option,
) (gin.HandlerFunc, error) {
	cfg, err := newConfig(opts)
	if err != nil {
		return nil, err
	}
	if reg == nil {
		return nil, errors.New("ginx: Handler needs a registry")
	}
	if principal == nil {
		return nil, fmt.Errorf(
			"ginx: %q needs a principal function; pass ginx.Anonymous to authenticate nobody", name)
	}

	// Resolved once, here, rather than on every request: the entry is immutable
	// after registration, and looking it up per request would put a map read and
	// a "does this still exist" branch on the hot path for a fact that cannot
	// change.
	entry, ok := reg.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("ginx: no spec named %q is registered", name)
	}
	// Checked when the handler is built, so a template naming a field the
	// output has no property for is refused here rather than found by whoever
	// followed the header. Mount gets this for free, and so does a handler
	// placed by hand on a router Mount never saw.
	if cfg.location != "" {
		if err := entry.CheckLocation(cfg.location); err != nil {
			return nil, fmt.Errorf("ginx: %w", err)
		}
	}

	return func(c *gin.Context) {
		who, err := principal(c)
		if err != nil {
			fail(c, err, cfg.onError)
			return
		}

		raw, err := payload(c, entry)
		if err != nil {
			fail(c, err, cfg.onError)
			return
		}

		// c.Request.Context(), not c. A *gin.Context is a context.Context, but
		// its Done channel is nil unless the engine sets ContextWithFallback,
		// so passing c would hand the kernel a context that never cancels and
		// keep a transaction open after the client has gone. Verified against
		// gin v1.12.0, Context.hasRequestContext.
		res, err := reg.Dispatch(c.Request.Context(), who, name, raw)
		if err != nil {
			fail(c, err, cfg.onError)
			return
		}

		// Result.Status is the spec's hint, read per request rather than
		// captured above, so that the override is the only thing this package
		// decides about the success status.
		code := res.Status
		if cfg.statusSet {
			code = cfg.status
		}

		// Encoded before anything is written, and this is the whole reason not
		// to call c.JSON. Gin's renderer commits the status first and encodes
		// second, so a value that cannot be encoded leaves the client holding a
		// 200 with an empty body while nothing here ever gets to answer 500 --
		// and an unencodable value is not exotic, since a float64 that came out
		// NaN is what an average over no rows is.
		//
		// encoding/json rather than Gin's codec indirection, so that this
		// adapter and the net/http one put the same bytes on the wire.
		body, err := json.Marshal(res.Value)
		if err != nil {
			fail(c, err, cfg.onError)
			return
		}

		// After the marshal, so a value that cannot be encoded is a 500 with no
		// Location rather than a 500 claiming something was created. The
		// net/http adapter reaches the same answer from the other direction --
		// it expands first and clears the header when the marshal fails -- and
		// the conformance suite is what holds the two to it.
		if cfg.location != "" {
			location, err := services.ExpandLocation(cfg.location, res.Value)
			if err != nil {
				fail(c, err, cfg.onError)
				return
			}
			c.Header("Location", location)
		}

		// c.Data rather than an aborting write: a handler registered after this
		// one is a deliberate choice by whoever built the chain, and success is
		// not a reason to cut it short. The failure paths do abort, for the
		// opposite reason. Gin still drops the body for a status that forbids
		// one, so a 204 stays empty.
		c.Data(code, jsonContentType, body)
	}, nil
}

// payload builds the JSON document the kernel validates out of the three
// places a Gin request carries input.
//
// It does not merge them. services.EncodeParams takes the body, the query and
// the captures separately and applies them in that order, so the rule that a
// route capture beats a query parameter is enforced by the kernel rather than
// restated here. That matters because the rule is a security one: a route like
// "/tenants/:tenant/reports" scopes the operation, and an adapter that merged
// the other way would let a client rescope it with "?tenant=other" while every
// test it has still passed.
//
// Coercion is EncodeParams' job for the same reason -- a query string is all
// strings, "?limit=10" has to become {"limit": 10}, and only the schema knows
// which. Two HTTP adapters that each guessed would be two adapters that
// disagree.
func payload(c *gin.Context, entry services.Entry) (json.RawMessage, error) {
	// Bounded before the first read rather than measured after it: by the time
	// an unbounded io.ReadAll could notice the size, it has already allocated
	// whatever the client chose to send. The ceiling is the kernel's constant
	// and not an option here, because two transports refusing at different
	// sizes is a difference no client can predict.
	limited := http.MaxBytesReader(unwrap(c.Writer), c.Request.Body, services.DefaultMaxBodyBytes)
	body, err := io.ReadAll(limited)
	if err != nil {
		// Recognising the transport's own oversize error is this package's job;
		// only the status and the sentence are shared.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, fmt.Errorf("%w: the limit is %d bytes", services.ErrBodyTooLarge, tooLarge.Limit)
		}
		// A truncated or aborted upload. The client is told that the body could
		// not be read and nothing else: an io error's text describes the
		// server's side of a socket, and it is answered as a validation failure
		// because the request, not the service, is what went wrong. The
		// sentence is the kernel's so that both HTTP adapters say it.
		return nil, services.Invalid(services.NonFieldKey, services.UnreadableBodyText)
	}

	// One value per capture. Gin will bind the same name twice if a pattern
	// uses it twice, and this keeps the last while c.Param returns the first --
	// so Mount refuses such a pattern rather than leaving a middleware reading
	// c.Param and the operation reading its input field to disagree about which
	// value is in force. A handler placed by hand is not covered, which is the
	// same boundary every other mount-time check has.
	//
	// A catch-all's value arrives with the leading slash Gin matched it from.
	// It is dropped so that ":tenant" and "*tenant" deliver the same string,
	// and so that this adapter agrees with net/http's "{tenant...}", which
	// yields no slash. A ":name" value can never begin with one, because it
	// matches inside a single segment.
	//
	// Query() builds a fresh url.Values on every call, so neither map handed to
	// the kernel is shared with anything.
	captures := make(map[string][]string, len(c.Params))
	for _, p := range c.Params {
		captures[p.Key] = []string{strings.TrimPrefix(p.Value, "/")}
	}
	return services.EncodeParams(entry.Input, body, c.Request.URL.Query(), captures)
}
