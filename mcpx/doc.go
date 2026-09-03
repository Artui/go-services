// Package mcpx mounts a services.Registry onto an MCP server as tools.
//
// It uses the official SDK's non-generic Server.AddTool, supplying the schema
// the kernel already reflected, so the schema a tool advertises is the schema
// the kernel enforces.
package mcpx
