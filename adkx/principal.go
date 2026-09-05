package adkx

import "google.golang.org/adk/v2/agent"

// Principal turns an ADK invocation into whatever the registry's own resolver
// expects. It runs before anything else, and returning an error wrapping
// services.ErrPermission is how it refuses a call.
type Principal func(agent.Context) (any, error)

// UserID is the ordinary choice: the user the ADK session belongs to.
//
// It is a function rather than a default because the kernel takes an opaque
// principal and only the application knows what one is. An application keying
// on the session or the app name writes its own, and gets the same typed Deps
// out of its resolver either way.
func UserID(ctx agent.Context) (any, error) { return ctx.UserID(), nil }

// Anonymous authenticates nobody, for a toolset whose registry needs no
// identity. It is spelled out at the call site so that an unauthenticated
// mount is something someone chose rather than something they forgot.
func Anonymous(agent.Context) (any, error) { return nil, nil }
