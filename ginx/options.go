package ginx

import (
	"fmt"

	"github.com/Artui/go-services"
	"github.com/gin-gonic/gin"
)

// Option configures a handler. The same options apply to Handler and to Mount,
// where they become the default for every route in the table.
type Option func(*config)

// config is what the options build. It is deliberately tiny: an adapter that
// grows knobs is an adapter that has started making decisions the kernel should
// be making for every transport.
type config struct {
	// status overrides the spec's own success status, and statusSet says whether
	// anything set it.
	//
	// A pair rather than a zero sentinel, because "nobody asked for an
	// override" and "somebody asked for status 0" are different mistakes. The
	// second is a caller computing a status from configuration and getting
	// nothing back, and it must be refused rather than quietly becoming the
	// spec's own status.
	status    int
	statusSet bool

	// location is a Location header template, or empty for no header. Unlike
	// status it needs no "was it set" flag: an empty template and "nobody
	// asked" are the same request, since a Location that is the empty string
	// is not a thing anyone can want.
	location string

	// onError observes an error this package could not map onto a status. It
	// exists because the 500 response deliberately says nothing.
	onError func(*gin.Context, error)
}

// WithStatus overrides the success status the spec declared. It must be one
// services.ValidSuccessStatus accepts, 200 to 599; that function's comment
// carries the evidence for why the floor is not 100.
//
// Passed to Mount it sets the default for every route in the table, and a
// Route.Status overrides it again for one route.
func WithStatus(code int) Option {
	return func(c *config) { c.status, c.statusSet = code, true }
}

// WithLocation sets a Location header on a successful response, from a template
// naming fields of the operation's output: "/loans/{loan_id}".
//
// The filling is services.ExpandLocation, so this adapter and the net/http one
// build the same header from the same output. That matters more than it looks:
// a client following a Location must not reach a different place depending on
// which router the server happens to be built with.
//
// The syntax is {name}, not Gin's :name. A route path is a pattern matched out
// of the request; this is a template filled from the response, and the two are
// unrelated enough that sharing a syntax would only suggest otherwise.
//
// Passed to Mount it sets the default for every route in the table, and a
// Route.Location overrides it again for one route.
func WithLocation(template string) Option {
	return func(c *config) { c.location = template }
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
	// The kernel's answer, not a local one. Register and both HTTP adapters need
	// this same judgement, and when two of them kept their own copy the two
	// copies disagreed.
	if cfg.statusSet && !services.ValidSuccessStatus(cfg.status) {
		return config{}, fmt.Errorf(
			"ginx: %d is not a status a response can be sent with; use 200 to 599", cfg.status)
	}
	return cfg, nil
}
