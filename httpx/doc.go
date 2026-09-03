// Package httpx mounts a services.Registry onto the standard library's
// net/http ServeMux.
//
// It carries no third-party dependency, so it reaches chi, echo and anything
// else that speaks net/http. It is also the measurement for whether the kernel
// is genuinely transport-neutral: a second adapter of the same shape should
// share everything but the wire-poking.
package httpx
