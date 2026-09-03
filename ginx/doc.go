// Package ginx mounts a services.Registry onto a Gin router.
//
// It exists because a Gin application already has gin.Context, its middleware
// chain and its own route table, not because it needs different logic from
// package httpx. If this package grows large, that is evidence about the
// kernel rather than about Gin.
//
// # Mounting
//
// A route table names a spec, a method and a path in Gin's own syntax:
//
//	err := ginx.Mount(engine.Group("/api"), registry, map[string]ginx.Route{
//	    "get_author":    {Method: "GET", Path: "/authors/:id"},
//	    "list_authors":  {Method: "GET", Path: "/authors"},
//	    "create_author": {Method: "POST", Path: "/authors"},
//	}, currentUser)
//
// currentUser is a func(*gin.Context) (any, error) returning whatever the
// registry's own resolver expects. It runs before anything else, and returning
// an error wrapping services.ErrPermission is how it refuses a request.
//
// Mount checks the whole table before registering any of it, so a spec that is
// not registered, a method that contradicts the spec's Kind, or a status no
// response can carry is an error at startup rather than a route that misbehaves
// once it is live.
//
// # Where input comes from
//
// A request carries input in three places: the body, the query string and the
// route's captures. This package hands all three to services.EncodeParams and
// does not merge them itself, so the rule that a capture beats a query
// parameter is the kernel's and not one more thing an adapter could get
// backwards. Coercion is the schema's business for the same reason --
// "?limit=10" becomes a number because the spec says the field is one.
//
// A capture the operation does not declare is refused rather than dropped, and
// Mount refuses it at startup: a route mounted at "/tenants/:tenant/invoices"
// against an operation with no tenant field would run unscoped on every request
// it served, so it never gets to serve one. A handler placed by hand on another
// router has no pattern for Mount to read, and there the same fault is a 500 at
// request time -- addressed to whoever wrote the route, because no change the
// caller could make would help. An unrecognised query parameter is still
// dropped: a query string carries noise nobody declared, while a capture was
// written into the route by hand and is always load-bearing.
//
// A catch-all capture ("*rest") arrives without the leading slash Gin matched
// it from, so it reads the same as the ":rest" form and the same as net/http's
// "{rest...}".
//
// The body is bounded at services.DefaultMaxBodyBytes before it is read.
//
// # What comes back
//
// A success is the service's return value as JSON, under the status the spec
// declared or the Route overrode. A failure is one of six answers: 400 with
// per-field messages, 403, 404 or 409 with the error's own words, 413 for a
// body over the ceiling, and 500 with a fixed sentence that says nothing about
// what went wrong. The real error behind a 500 goes to the WithErrorHandler
// callback and to c.Errors, never to the client.
package ginx
