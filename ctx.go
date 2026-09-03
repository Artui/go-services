package services

import "context"

// Ctx is the per-call pool handed to a service and to its Permit functions.
//
// It is the whole of djangorestframework-services' signature-based keyword
// binding collapsed into one typed value, because Go cannot bind by parameter
// name. It is also what makes the fat-service-struct footgun unrepresentable:
// dependencies arrive per call, never per constructed object, so a service
// pays only for what the call actually resolved.
//
// It carries no actor. Identity is a field on D, put there by the Registry's
// resolver -- see New. That keeps one type parameter here rather than two, and
// keeps the kernel out of the business of having an opinion about identity.
type Ctx[D any] struct {
	// Context is the call's context. When the spec is atomic it is the
	// transaction-carrying context, because Deps resolve inside the boundary.
	Context context.Context

	// Deps is whatever the resolver built for this call.
	Deps D
}
