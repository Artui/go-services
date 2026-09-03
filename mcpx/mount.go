package mcpx

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Artui/go-services"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A Principal authenticates one tool call, returning the opaque value the
// Registry's own resolver turns into typed dependencies.
//
// It takes the whole request rather than just a context because that is where
// the credential is: RequestExtra carries the HTTP header and the OAuth token
// info for a streamable mount, and Session identifies the connection. What it
// returns is deliberately untyped -- the kernel's resolver is the one place an
// application asserts its own identity type, and putting a second opinion here
// would give a mount a way to disagree with it.
//
// Returning an error refuses the call. The error travels the same taxonomy as
// any other failure, so wrapping services.ErrPermission produces a refusal the
// model can read, and anything else produces the fixed internal sentence.
type Principal func(context.Context, *mcp.CallToolRequest) (any, error)

// An ErrorReporter observes the errors mcpx deliberately keeps off the wire.
//
// It is called only for failures outside the kernel's taxonomy -- the ones a
// tool result renders as InternalErrorText. Recognised refusals are not
// reported, because those are already visible to the caller; this exists so
// that redacting an unexpected error does not also lose it.
type ErrorReporter func(ctx context.Context, tool string, err error)

// An Option configures a mount.
type Option func(*mount)

// WithErrorReporter registers fn to receive every error mcpx replaces with
// InternalErrorText.
//
// A mount without one redacts unexpected failures to the client and then drops
// them, which is the worse half of two reasonable behaviours.
func WithErrorReporter(fn ErrorReporter) Option {
	return func(m *mount) { m.report = fn }
}

// mount holds the per-mount configuration. It is not parameterised by D: none
// of it touches the dependency type, so keeping it plain lets Option stay
// non-generic and readable at a call site.
type mount struct {
	principal Principal
	report    ErrorReporter
}

// Mount adds every spec in reg to srv as an MCP tool.
//
// The tools it registers advertise the schemas the kernel reflected at
// registration, and their handlers call the kernel to run. Nothing between the
// two revalidates, remaps or reinterprets: validation, permissions, the
// transaction boundary and the error taxonomy all stay where they were, which
// is the point of the adapter being this thin.
//
// principal may be nil, in which case the kernel's resolver is handed a nil
// principal. That is correct for a registry whose dependencies carry no
// identity, and wrong for every other one, so it is a positional argument
// rather than an option -- a mount that does not authenticate has to say so.
//
// Tools are added in the registry's registration order, though the SDK sorts
// its tools/list response by name and a client sees that order instead. Mount
// returns an error and adds nothing if any spec cannot be published, so a
// registry with one bad entry does not leave a half-populated server behind.
//
// It reads reg once. A spec registered afterwards is not advertised, because
// the kernel has no vocabulary for announcing that its own contents changed and
// inventing one in an adapter would put it in the wrong place. Register
// everything, then mount.
func Mount[D any](
	srv *mcp.Server, reg *services.Registry[D], principal Principal, opts ...Option,
) error {
	m := &mount{principal: principal}
	for _, opt := range opts {
		opt(m)
	}

	entries := reg.Entries()
	ready := make([]pending, 0, len(entries))
	for _, e := range entries {
		tool, err := toolFor(e)
		if err != nil {
			return err
		}
		ready = append(ready, pending{tool: tool, handler: handlerFor(m, reg, e.Name)})
	}
	if err := rehearse(ready); err != nil {
		return err
	}

	// Registration is the last pass on purpose. AddTool notifies connected
	// clients of a tool-list change as it goes, so failing partway through an
	// earlier pass would already have advertised the tools it got to.
	for _, p := range ready {
		srv.AddTool(p.tool, p.handler)
	}
	return nil
}

// pending is a tool definition and its handler, checked but not yet registered.
type pending struct {
	tool    *mcp.Tool
	handler mcp.ToolHandler
}

// rehearse adds every tool to a throwaway server first, so that an AddTool
// panic becomes an error before the real server has advertised anything.
//
// toolFor catches the two conditions worth a good error message -- an unusable
// name and a non-object input schema -- but AddTool panics on more than those.
// It also rejects a malformed x-mcp-header extension, which is reachable
// through Spec.Schema, since writing into jsonschema.Schema.Extra is the only
// way to express that MCP feature at all. Without this pass, such a spec
// panics out of Mount with earlier tools already registered and clients already
// notified, which is the exact outcome the staged registration exists to
// prevent.
//
// Rehearsing rather than reimplementing the SDK's checks is deliberate. Those
// checks are internal, version-specific and not part of any contract -- the
// header rules live in the SDK's streamable transport file -- so a copy here
// would be correct only until the next bump, and wrong silently. Running the
// SDK's own validation cannot drift from it. The cost is a second AddTool per
// tool, paid once at wiring.
func rehearse(ready []pending) error {
	// The scratch server discards its logs: AddTool logs an invalid name rather
	// than panicking, and that condition is already an error from toolFor, so
	// anything it would write here is a duplicate on somebody's stderr.
	scratch := mcp.NewServer(
		&mcp.Implementation{Name: "mcpx-rehearsal", Version: "v0"},
		&mcp.ServerOptions{Logger: slog.New(slog.DiscardHandler)},
	)
	for _, p := range ready {
		if err := addOnce(scratch, p); err != nil {
			return err
		}
	}
	return nil
}

// addOnce performs one AddTool, converting a panic into an error naming the
// spec that caused it. The SDK's panic message says what was wrong but not
// which spec it came from, and a registry has many.
func addOnce(srv *mcp.Server, p pending) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("mcpx: %q: the MCP SDK rejected this tool definition: %v", p.tool.Name, r)
		}
	}()
	srv.AddTool(p.tool, p.handler)
	return nil
}

// handlerFor builds the tool handler for one spec.
//
// It is a function rather than a method because it needs D and Go has no
// generic methods; m carries everything that does not.
//
// The handler is the SDK's non-generic ToolHandler, which is the whole reason
// this adapter can keep its promise. The generic AddTool would reflect In and
// Out a second time from types the kernel has already erased, producing a
// schema that is advertised and validated against while a different one is
// enforced. The non-generic form passes the arguments through untouched, and
// the kernel is what decides whether they are acceptable.
func handlerFor[D any](m *mount, reg *services.Registry[D], name string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var principal any
		if m.principal != nil {
			p, err := m.principal(ctx, req)
			if err != nil {
				return m.failed(ctx, name, err), nil
			}
			principal = p
		}

		// Dispatch, not DispatchValue: the SDK hands a non-generic handler the
		// arguments as raw JSON, so decoding them into a map here only to have
		// the kernel encode them back would round-trip every integer through a
		// float64 and lose the ones that do not fit. The bytes the client sent
		// are the bytes the kernel validates.
		res, err := reg.Dispatch(ctx, principal, name, req.Params.Arguments)
		if err != nil {
			return m.failed(ctx, name, err), nil
		}

		result, err := succeed(res)
		if err != nil {
			return m.failed(ctx, name, err), nil
		}
		return result, nil
	}
}

// failed renders err as a tool result and, when the taxonomy did not recognise
// it, hands the real error to the reporter before it is redacted.
//
// The nil error return at every call site is not an oversight: a tool result
// with IsError set is how a failure reaches the model, and returning a Go error
// instead would turn it into a JSON-RPC protocol error the model never sees.
func (m *mount) failed(ctx context.Context, tool string, err error) *mcp.CallToolResult {
	result, known := refuse(ctx, err)
	if !known && m.report != nil {
		m.report(ctx, tool, err)
	}
	return result
}
