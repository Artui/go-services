package ginx

import (
	"fmt"

	"github.com/gin-gonic/gin"
)

// Option configures a handler. The same options apply to Handler and to Mount,
// where they become the default for every route in the table.
type Option func(*config)

// config is what the options build. It is deliberately tiny: an adapter that
// grows knobs is an adapter that has started making decisions the kernel should
// be making for every transport.
type config struct {
	// status overrides the spec's own success status. Zero means "no override",
	// which is why WithStatus refuses a zero rather than storing it.
	status int

	// onError observes an error this package could not map onto a status. It
	// exists because the 500 response deliberately says nothing.
	onError func(*gin.Context, error)
}

// WithStatus overrides the success status the spec declared.
//
// Passed to Mount it sets the default for every route in the table, and a
// Route.Status overrides it again for one route.
func WithStatus(code int) Option {
	return func(c *config) { c.status = code }
}

// WithErrorHandler registers fn to receive the errors this package answers with
// a 500 -- the ones whose text the client never sees.
//
// It is not a general request hook: a 400, 403, 404 or 409 already told the
// client what happened, and reporting those here would drown the case that
// matters in ordinary refusals. Every error, mapped or not, is also pushed onto
// c.Errors, so a Gin application that already has an error middleware needs no
// option at all.
func WithErrorHandler(fn func(*gin.Context, error)) Option {
	return func(c *config) { c.onError = fn }
}

// newConfig applies the options and rejects a configuration that could only
// fail later, at request time, in front of a client.
func newConfig(opts []Option) (config, error) {
	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.status != 0 && !validStatus(cfg.status) {
		return config{}, fmt.Errorf("ginx: %d is not an HTTP status code", cfg.status)
	}
	return cfg, nil
}

// validStatus reports whether code is a status a response may actually carry.
//
// net/http panics on a code outside 100-999, and a code in 600-999 is not a
// status any client understands, so the range is checked here where the answer
// is a returned error rather than a panic from inside a handler.
func validStatus(code int) bool { return code >= 100 && code <= 599 }
