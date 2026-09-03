// Package httpx mounts a services.Registry onto the standard library's
// net/http ServeMux.
//
// It carries no third-party dependency, so it reaches chi, echo and anything
// else that speaks net/http. It is also the measurement for whether the kernel
// is genuinely transport-neutral: a second adapter of the same shape should
// share everything but the wire-poking.
//
// # The wire
//
// A success is Result.Value as JSON under the spec's declared status, or the
// route's override. A failure is one of:
//
//	400  {"errors": {"field": ["message"]}}   a validation failure, any layer
//	403  {"error": "..."}                     services.ErrPermission
//	404  {"error": "..."}                     services.ErrNotFound
//	409  {"error": "..."}                     services.ErrConflict
//	413  {"error": "request body too large"}  over services.DefaultMaxBodyBytes
//	500  {"error": "internal server error"}   anything else, always this sentence
//
// The 500 is the one that is fixed rather than reported. An unexpected error's
// words are written for an operator and name hosts, tables and identifiers, so
// they go to WithOnError instead of to the client. A mount that configures no
// observer redacts the failure and then drops it, which is worth knowing before
// the first incident rather than during it.
//
// A misconfigured route answers 500 for the same reason and not 400: the
// deployment is wrong, no change the caller makes will help, and a 400 would
// tell them to fix a request that was never the problem.
//
// # What is checked, and when
//
// Mount refuses at start-up what would otherwise fail at request time: a name
// no spec is registered under, a method the spec's Kind may not be mounted on,
// a pattern capturing a segment the input has no field for, two specs claiming
// one method and path, a status that cannot be sent. It reports every problem
// in the table at once and mounts nothing unless all of them pass.
//
// The capture check also exists in the kernel, at dispatch, and the two are not
// duplicates. Mount holds the patterns and can refuse a route that would be
// broken in every request it ever served; a bare Handler behind a router with
// its own capture syntax has no pattern for Mount to read, and the
// dispatch-time refusal is what covers that one.
//
// # Where the input comes from
//
// Body, then query string, then path captures, each overwriting the last, so a
// route capture always wins. The merge, the type coercion and the refusal of an
// undeclared capture are all the kernel's -- see services.EncodeParams --
// because a query string is all strings, only the schema knows which of them is
// a number, and a capture that goes nowhere is a scope that silently vanished.
package httpx
