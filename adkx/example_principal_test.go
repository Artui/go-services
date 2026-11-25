package adkx_test

import (
	"context"
	"fmt"
	"log"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// tenantKey is where the server's middleware put the request's tenant.
type tenantKey struct{}

// caller is what the principal reads off one tool call, and here also the Deps.
type caller struct{ user, tenant, callID string }

type bookIn struct {
	Room int `json:"room"`
}

// A principal reads a request-scoped value and the tool-call id, and the
// resolver hands both to the spec.
func Example_principal() {
	reg := services.New(func(_ context.Context, who any) (caller, error) {
		c, ok := who.(caller)
		if !ok {
			return caller{}, services.ErrPermission
		}
		return c, nil
	})
	services.MustRegister(reg, services.Spec[caller, bookIn, string]{
		Name: "book", Description: "Book a room.", Kind: services.Mutation,
		Run: func(c services.Ctx[caller], in bookIn) (string, error) {
			fmt.Printf("room %d for %s of %s, call %s\n", in.Room, c.Deps.user, c.Deps.tenant, c.Deps.callID)
			return "booked", nil
		},
	})
	specs, err := adkx.Toolset(reg, func(ctx agent.Context) (any, error) {
		tenant, ok := ctx.Value(tenantKey{}).(string)
		if !ok {
			return nil, services.ErrPermission
		}
		return caller{user: ctx.UserID(), tenant: tenant, callID: ctx.FunctionCallID()}, nil
	})
	if err != nil {
		log.Fatal(err)
	}

	// A stand-in for a model: it calls book once, then answers.
	llm := &scriptedLLM{calls: []*genai.FunctionCall{
		{ID: "toolu_abc", Name: "book", Args: map[string]any{"room": 7}},
	}}
	root, err := llmagent.New(llmagent.Config{Name: "desk", Model: llm, Toolsets: []tool.Toolset{specs}})
	if err != nil {
		log.Fatal(err)
	}
	r, err := runner.New(runner.Config{
		AppName: "app", Agent: root,
		SessionService: session.InMemoryService(), AutoCreateSession: true,
	})
	if err != nil {
		log.Fatal(err)
	}
	// The context the agent runs under is the one the principal reads.
	ctx := context.WithValue(context.Background(), tenantKey{}, "acme")
	for _, err := range r.Run(ctx, "ada", "s1", genai.NewContentFromText("go", "user"), agent.RunConfig{}) {
		if err != nil {
			log.Fatal(err)
		}
	}
	// Output: room 7 for ada of acme, call toolu_abc
}
