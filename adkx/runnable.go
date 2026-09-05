package adkx

import (
	"google.golang.org/adk/v2/agent"
	adktool "google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// RunnableTool is the shape ADK actually dispatches on: a tool that declares
// itself to a model and can be called.
//
// ADK's own version of this interface is unexported, and it matches a tool
// structurally at the point of use -- so nothing in the compiler tells a
// consumer that a method drifted, and there is no name to assert against. Every
// consumer that wants to drive a tool directly, which mostly means every
// consumer that wants to test one, ends up writing this interface out. Three
// copies of it existed in this repository before it was exported: adkx's own
// suite, the conformance driver, and the worked example.
//
// Declaring it here gives them one name, and makes this package the single
// place that has to notice if ADK's shape changes. The assertion below is what
// turns such a change into a build failure here rather than a runtime surprise
// in somebody's agent.
//
//	tools, _ := toolset.Tools(ctx)
//	for _, published := range tools {
//	    if runnable, ok := published.(adkx.RunnableTool); ok {
//	        result, err := runnable.Run(ctx, map[string]any{"book_id": 4})
//	    }
//	}
//
// It is not a promise about ADK's API, which is not ours to make. It is a
// promise that what this package publishes is runnable, which is.
type RunnableTool interface {
	adktool.Tool

	// Declaration is what the model is shown: the tool's name, its description
	// and the schema the kernel reflected for its input.
	Declaration() *genai.FunctionDeclaration

	// Run executes the tool. ADK passes arguments as a map[string]any -- see
	// this package's own documentation for what that costs.
	Run(ctx agent.Context, args any) (map[string]any, error)
}

// Every tool this package publishes is runnable, checked at compile time.
//
// The type argument is arbitrary: specTool's method set does not depend on D,
// so one instantiation proves it for all of them.
var _ RunnableTool = (*specTool[struct{}])(nil)
