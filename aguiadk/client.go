package aguiadk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

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
func ClientToolset() tool.Toolset {
	return clientToolset{}
}

type clientToolsKey struct{}

func withClientTools(ctx context.Context, tools []*clientTool) context.Context {
	return context.WithValue(ctx, clientToolsKey{}, tools)
}

func clientToolsFrom(ctx context.Context) []*clientTool {
	tools, _ := ctx.Value(clientToolsKey{}).([]*clientTool)
	return tools
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

type clientToolset struct{}

func (clientToolset) Name() string { return "client" }

func (clientToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	var tools []tool.Tool
	for _, t := range clientToolsFrom(ctx) {
		tools = append(tools, t)
	}
	return tools, nil
}

// ProcessRequest declares the client tools. ADK runs it after every tool's own
// ProcessRequest, so a name already taken belongs to a server tool and is kept.
func (clientToolset) ProcessRequest(ctx agent.Context, req *model.LLMRequest) error {
	for _, t := range clientToolsFrom(ctx) {
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
