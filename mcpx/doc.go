// Package mcpx mounts a services.Registry onto an MCP server as tools.
//
// It uses the official SDK's non-generic Server.AddTool, supplying the schema
// the kernel already reflected, so the schema a tool advertises is the schema
// the kernel enforces. The generic AddTool would reflect the input and output
// types a second time from types the Registry has already erased, which is how
// a transport ends up validating against a contract that is not the one being
// enforced.
//
// There is no HTTP hop. A spec mounted here is the same declaration an HTTP
// adapter serves, run through the same validation, permissions and transaction
// boundary, with only the wire format differing.
//
// Two contracts are worth reading before wiring one up:
//
// A failure is a result, not a protocol error. A service that declined has
// answered, and the answer is one the model must read and react to, so it
// arrives as a CallToolResult with IsError set and a readable message. Only a
// tool that does not exist is a JSON-RPC error, and the SDK raises that before
// the mount is involved. Validation failures render their field messages so a
// model can correct itself and call again.
//
// An unexpected failure is redacted. Anything outside the kernel's error
// taxonomy reaches the client as InternalErrorText, because an internal error's
// words are written for an operator. WithErrorReporter is how the real error is
// still observed; a mount without one redacts the failure and then drops it.
package mcpx
