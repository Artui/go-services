// Package adkx exposes a services.Registry as tools for Google's Agent
// Development Kit.
//
// It is the same trade package mcpx makes, and it is small for the same reason:
// adk-go reflects its schemas with github.com/google/jsonschema-go, at the same
// version this kernel does, and genai.FunctionDeclaration carries a
// ParametersJsonSchema field. So the schema the kernel already reflected is
// handed over as it is, rather than derived a second time from the same struct
// by different code that would eventually disagree.
//
//	toolset, err := adkx.Toolset(registry, adkx.UserID)
//	agent := llmagent.New("librarian", llmagent.WithToolsets(toolset))
//
// # Two things about this wire are worth knowing before you use it
//
// Arguments arrive as a map, not as bytes. genai.FunctionCall.Args is a
// map[string]any, so a model's tool-call arguments are decoded before any tool
// in any ADK program sees them, and an integer past 2^53 has already become a
// float64 by then. That is upstream of this package and it cannot be fixed
// here: the same registry is exact over MCP, whose SDK carries the raw JSON,
// and lossy over ADK. Declare an identifier as a string if it can exceed 2^53.
//
// A tool's error text is shown to the model. ADK renders a returned error as
// map[string]any{"error": err.Error()} and hands it back to the LLM, so an
// adapter that returned errors as it found them would put connection strings
// and table names in front of a model that will repeat or act on them. This
// package returns the kernel's taxonomy verbatim -- those words were written
// for whoever made the call -- and replaces everything else with a fixed
// sentence, sending the real error to WithErrorReporter instead.
package adkx
