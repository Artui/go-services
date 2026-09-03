package ginx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Artui/go-services"
	"github.com/gin-gonic/gin"
)

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
		if cfg.status != 0 {
			code = cfg.status
		}

		// c.JSON rather than AbortWithStatusJSON: a handler registered after
		// this one is a deliberate choice by whoever built the chain, and
		// success is not a reason to cut it short. The failure paths do abort,
		// for the opposite reason.
		c.JSON(code, res.Value)
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
	limited := http.MaxBytesReader(c.Writer, c.Request.Body, services.DefaultMaxBodyBytes)
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

	// One value per capture, because a Gin route cannot bind the same name
	// twice. Query() builds a fresh url.Values on every call, so neither map
	// handed to the kernel is shared with anything.
	captures := make(map[string][]string, len(c.Params))
	for _, p := range c.Params {
		captures[p.Key] = []string{p.Value}
	}
	return services.EncodeParams(entry.Input, body, c.Request.URL.Query(), captures)
}
