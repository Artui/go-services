// Package aguiadk serves an ADK agent over AG-UI: one aguix run is one ADK
// invocation, rebuilt from the conversation the client sends.
//
// ADK's tool confirmation (RequireConfirmation and the adk_request_confirmation
// long-running call) is not supported: the request would reach the browser as a
// tool call it never declared.
package aguiadk
