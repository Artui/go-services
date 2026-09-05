package adkx

import (
	"errors"

	services "github.com/Artui/go-services"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
)

// ErrorReporter receives the errors this package answers with a fixed sentence
// -- the ones whose text the model never sees.
type ErrorReporter func(ctx agent.Context, toolName string, err error)

// Option configures a toolset.
type Option func(*toolset)

// WithErrorReporter registers fn to receive every error outside the kernel's
// taxonomy.
//
// It is not a general hook. A permission refusal or a validation failure has
// already told the model what happened and reporting those here would bury the
// case that matters. A toolset with no reporter answers the model with a fixed
// sentence and then drops the real failure, which is worth knowing before the
// first incident rather than during it.
func WithErrorReporter(fn ErrorReporter) Option {
	return func(t *toolset) { t.report = fn }
}

// WithName sets the toolset's name, which ADK uses to group and filter tools.
func WithName(name string) Option {
	return func(t *toolset) { t.name = name }
}

// Toolset exposes every spec in reg as an ADK tool.
//
// It reads reg once and builds every tool up front, returning an error and no
// toolset if any spec cannot be published -- so a registry with one bad entry
// does not leave an agent holding a half-populated set it will call anyway. A
// spec registered afterwards is not published, because the kernel has no
// vocabulary for announcing that its own contents changed.
func Toolset[D any](
	reg *services.Registry[D], principal Principal, opts ...Option,
) (tool.Toolset, error) {
	if reg == nil {
		return nil, errors.New("adkx: Toolset needs a registry")
	}
	if principal == nil {
		return nil, errors.New(
			"adkx: Toolset needs a principal function; pass adkx.Anonymous to authenticate nobody")
	}

	ts := &toolset{name: "services"}
	for _, opt := range opts {
		opt(ts)
	}

	for _, entry := range reg.Entries() {
		ts.tools = append(ts.tools, newTool(reg, entry, principal, ts))
	}
	return ts, nil
}

// toolset is the built set. Every field is set at construction and read-only
// afterwards, so one toolset serves concurrent invocations with no state.
type toolset struct {
	name   string
	tools  []tool.Tool
	report ErrorReporter
}

func (t *toolset) Name() string { return t.name }

// Tools returns the whole set on every invocation.
//
// ADK allows this to vary per invocation, and it deliberately does not here:
// which operations exist is the registry's business, and deciding it per call
// would put an authorisation model in the adapter, where the kernel's Permit
// layer cannot see it and cannot enforce it. A caller wanting a subset composes
// tool.FilterToolset around this one, which is ADK's own answer.
func (t *toolset) Tools(agent.ReadonlyContext) ([]tool.Tool, error) { return t.tools, nil }

func (t *toolset) observe(ctx agent.Context, name string, err error) {
	if t.report != nil {
		t.report(ctx, name, err)
	}
}

// assertion that the toolset is what ADK expects.
var _ tool.Toolset = (*toolset)(nil)
