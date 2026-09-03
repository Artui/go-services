package httpx

import "net/http"

// Option configures a handler. Mount applies its options to every handler it
// mounts.
type Option func(*config)

// config is the resolved set of options. It is a value, not a pointer, on the
// handler: nothing mutates it after construction, so nothing has to
// synchronise it across the goroutines net/http serves requests on.
//
// Note what is not in it: the request body limit. That is
// services.DefaultMaxBodyBytes for every mount, because the size at which a
// request is refused is one decision for the whole library rather than a knob
// each deployment sets differently on each transport.
type config struct {
	status     int
	onError    func(*http.Request, int, error)
	pathValues func(*http.Request) map[string][]string
}

func newConfig(opts []Option) config {
	c := config{pathValues: patternValues}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// WithStatus overrides the success status the spec declared.
//
// It is here for the route that has to differ from the spec -- a create
// answering 201 on one mount and 200 on another. Route.Status is the same
// setting expressed per route, and takes precedence over this one.
func WithStatus(status int) Option {
	return func(c *config) { c.status = status }
}

// WithOnError hands every failed request to fn, with the status that was sent
// and the error as it actually was.
//
// It exists for the 500 case above all: that response says only
// "internal server error", so without an observer the failure is redacted from
// the client and then dropped on the floor. fn is called for the client's own
// mistakes too, with the status they were given, so an audit of refused
// requests needs no second hook and no guess at this adapter's mapping table.
//
// fn runs on the serving goroutine, before the response is written. It must
// not block.
func WithOnError(fn func(r *http.Request, status int, err error)) Option {
	return func(c *config) { c.onError = fn }
}

// WithPathValues replaces how a handler reads path captures.
//
// The default reads them off http.Request.Pattern, which only a ServeMux sets.
// A router with its own capture syntax -- chi, gorilla, anything wrapping
// net/http -- supplies them here instead, and everything downstream is
// unchanged: the kernel still coerces each capture against the input schema and
// still lets it win over the query string.
//
// A nil fn restores the default, so a computed option cannot silently disable
// path binding -- which would not fail, it would just quietly unscope every
// route that had a capture.
func WithPathValues(fn func(*http.Request) map[string][]string) Option {
	return func(c *config) {
		if fn == nil {
			fn = patternValues
		}
		c.pathValues = fn
	}
}
