package mcpx_test

import (
	"context"
	"fmt"
	"log"

	"github.com/Artui/go-services"
	"github.com/Artui/go-services/mcpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// session is the application's own dependency type. Identity lives on it,
// because the Registry's resolver is the one place an application says what a
// principal is.
type session struct {
	user string
}

type archiveIn struct {
	ID     int    `json:"id" jsonschema:"the note to archive"`
	Reason string `json:"reason,omitempty" jsonschema:"why, for the audit trail"`
}

type archiveOut struct {
	ID         int    `json:"id"`
	ArchivedBy string `json:"archived_by"`
}

// Example mounts a registry on an MCP server and calls one of its tools.
//
// The declaration is the whole configuration: Kind decides the read-only hint
// and the transaction default, Idempotent and Destructive decide which hints
// are published at all, and the reflected input type is both what a client is
// shown and what the kernel enforces. Nothing in this file restates any of it.
//
// Destructive is worth setting on anything additive. MCP's destructiveHint
// defaults to true for every non-read-only tool, so a create that says nothing
// is advertised as possibly destructive and an approval gate keyed on the hint
// prompts for it every time.
func Example() {
	registry := services.New(func(_ context.Context, principal any) (session, error) {
		user, ok := principal.(string)
		if !ok {
			return session{}, fmt.Errorf("%w: this connection is not signed in", services.ErrPermission)
		}
		return session{user: user}, nil
	})

	err := services.Register(registry, services.Spec[session, archiveIn, archiveOut]{
		Name:        "notes.archive",
		Description: "Archive a note. The note stays readable and stops appearing in listings.",
		Kind:        services.Mutation,
		Idempotent:  truth(true),
		Destructive: truth(false),
		Permit: []func(services.Ctx[session], archiveIn) error{
			func(c services.Ctx[session], in archiveIn) error {
				if in.ID == 13 {
					return fmt.Errorf("%w: note 13 belongs to someone else", services.ErrPermission)
				}
				return nil
			},
		},
		Run: func(c services.Ctx[session], in archiveIn) (archiveOut, error) {
			return archiveOut{ID: in.ID, ArchivedBy: c.Deps.user}, nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "notes", Version: "v1"}, nil)

	// The Principal turns whatever the transport authenticated into the opaque
	// value the resolver above expects. A streamable mount would read it off
	// req.Extra.TokenInfo; this one is a stand-in.
	principal := func(context.Context, *mcp.CallToolRequest) (any, error) {
		return "ursula", nil
	}

	err = mcpx.Mount(server, registry, principal, mcpx.WithErrorReporter(
		func(_ context.Context, tool string, err error) {
			log.Printf("mcpx: %s failed: %v", tool, err)
		},
	))
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		log.Fatal(err)
	}
	client, err := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "v1"}, nil).
		Connect(ctx, clientTransport, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	listed, err := client.ListTools(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	for _, tool := range listed.Tools {
		fmt.Printf("%s readOnly=%t idempotent=%t destructive=%t\n",
			tool.Name, tool.Annotations.ReadOnlyHint, tool.Annotations.IdempotentHint,
			*tool.Annotations.DestructiveHint)
	}

	archived, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name:      "notes.archive",
		Arguments: map[string]any{"id": 7},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(archived.Content[0].(*mcp.TextContent).Text)

	// A Permit refusal is a result the model reads, not a transport failure.
	refused, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name:      "notes.archive",
		Arguments: map[string]any{"id": 13},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(refused.IsError, refused.Content[0].(*mcp.TextContent).Text)

	// So is a payload the schema rejects, rendered so the model can retry.
	rejected, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name:      "notes.archive",
		Arguments: map[string]any{"id": "seven"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(rejected.IsError)
	fmt.Println(rejected.Content[0].(*mcp.TextContent).Text)

	// Output:
	// notes.archive readOnly=false idempotent=true destructive=false
	// {"id":7,"archived_by":"ursula"}
	// true services: permission denied: note 13 belongs to someone else
	// true
	// The arguments were rejected. Correct these and call the tool again:
	// - validating root: validating /properties/id: type: seven has type "string", want "integer"
}
