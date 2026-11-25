package aguiadk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolutils"
	"google.golang.org/genai"
)

// ClientToolset returns the tools the browser declared for the current run.
//
// Add it to the agent beside the server's own toolset. Each tool a run's input
// lists becomes a long-running tool that the agent may call and the server never
// answers: the call is streamed to the client, the run finishes with it pending,
// and the client's answer arrives as a tool message in the next run. Outside a
// run served by this package the toolset holds no tools.
//
// A browser tool named like a server tool is dropped, so the server's tool is
// the one the model sees and the one a call runs, whichever order the agent
// lists its toolsets in.
//
// With no AllowClientTools option every browser tool the run lists is declared,
// so the page, or any script on it, chooses what the model can call.
func ClientToolset(opts ...ClientToolOption) tool.Toolset {
	s := &clientToolset{}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ClientToolOption configures ClientToolset.
type ClientToolOption func(*clientToolset)

// AllowClientTools limits the toolset to the browser tools with these names: any
// other tool a run's input lists is neither declared to the model nor callable.
// Given more than once the names add up, and given no names it allows none.
//
// It bounds names only. The description and parameters of an allowed tool are
// still the ones the client sent.
func AllowClientTools(names ...string) ClientToolOption {
	return func(s *clientToolset) {
		if s.allowed == nil {
			s.allowed = map[string]bool{}
		}
		for _, name := range names {
			s.allowed[name] = true
		}
	}
}

// OnDroppedClientTool calls fn for each browser tool AllowClientTools leaves out,
// once per name in a run that reaches the model, with the run's context. The
// name is the client's own text. A tool dropped because a server tool has its
// name is not reported.
func OnDroppedClientTool(fn func(ctx context.Context, name string)) ClientToolOption {
	return func(s *clientToolset) { s.onDropped = fn }
}

type clientToolsKey struct{}

// clientRun is one run's browser tools, and the drops already reported in it.
type clientRun struct {
	tools    []*clientTool
	mu       sync.Mutex
	reported map[droppedTool]bool
}

// droppedTool names the toolset as well, since each has its own allow-list.
type droppedTool struct {
	set  *clientToolset
	name string
}

// first reports whether this is the run's first sight of the drop.
func (r *clientRun) first(d droppedTool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := r.reported[d]
	r.reported[d] = true
	return !seen
}

func withClientTools(ctx context.Context, tools []*clientTool) context.Context {
	return context.WithValue(ctx, clientToolsKey{}, &clientRun{tools: tools, reported: map[droppedTool]bool{}})
}

// clientRunFrom is empty outside a run served by this package.
func clientRunFrom(ctx context.Context) *clientRun {
	if run, ok := ctx.Value(clientToolsKey{}).(*clientRun); ok {
		return run
	}
	return &clientRun{}
}

// clientTools turns a run's tool definitions into tools, keeping the first of
// any two that share a name.
func clientTools(defs []aguix.ToolDefinition) ([]*clientTool, error) {
	var tools []*clientTool
	seen := map[string]bool{}
	for _, def := range defs {
		if seen[def.Name] {
			continue
		}
		seen[def.Name] = true
		decl := &genai.FunctionDeclaration{Name: def.Name, Description: def.Description}
		if raw := bytes.TrimSpace(def.Parameters); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil {
				return nil, fmt.Errorf("aguiadk: the parameters of client tool %q are not a JSON object", def.Name)
			}
			decl.ParametersJsonSchema = schema
		}
		tools = append(tools, &clientTool{decl: decl})
	}
	return tools, nil
}

type clientToolset struct {
	allowed   map[string]bool // nil allows every name
	onDropped func(ctx context.Context, name string)
}

func (*clientToolset) Name() string { return "client" }

func (s *clientToolset) allows(name string) bool { return s.allowed == nil || s.allowed[name] }

func (s *clientToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	var tools []tool.Tool
	for _, t := range clientRunFrom(ctx).tools {
		if s.allows(t.Name()) {
			tools = append(tools, t)
		}
	}
	return tools, nil
}

// ProcessRequest declares the client tools. ADK runs it after every tool's own
// ProcessRequest, so a name already taken belongs to a server tool and is kept.
// It runs before every model call, so a drop is reported only the first time.
func (s *clientToolset) ProcessRequest(ctx agent.Context, req *model.LLMRequest) error {
	run := clientRunFrom(ctx)
	for _, t := range run.tools {
		if !s.allows(t.Name()) {
			if s.onDropped != nil && run.first(droppedTool{s, t.Name()}) {
				s.onDropped(ctx, t.Name())
			}
			continue
		}
		if _, taken := req.Tools[t.Name()]; taken {
			continue
		}
		if err := toolutils.PackTool(req, t); err != nil {
			return err
		}
	}
	return nil
}

// clientTool is one browser tool. ADK sends no response for a long-running tool
// whose Run returns nothing, which leaves the call open for the client.
type clientTool struct {
	decl *genai.FunctionDeclaration
}

func (t *clientTool) Name() string                            { return t.decl.Name }
func (t *clientTool) Description() string                     { return t.decl.Description }
func (t *clientTool) IsLongRunning() bool                     { return true }
func (t *clientTool) Declaration() *genai.FunctionDeclaration { return t.decl }

func (t *clientTool) Run(agent.Context, any) (map[string]any, error) { return nil, nil }

// ProcessRequest is required of every tool; the toolset declares this one.
func (t *clientTool) ProcessRequest(agent.Context, *model.LLMRequest) error { return nil }
