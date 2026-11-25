package adkx_test

import (
	"context"
	"fmt"
	"iter"
	"log"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
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

// bookOnce stands in for a model: it calls book, then answers.
type bookOnce struct{}

func (bookOnce) Name() string { return "book-once" }

func (bookOnce) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		reply := genai.NewPartFromFunctionCall("book", map[string]any{"room": 7})
		reply.FunctionCall.ID = "toolu_abc"
		for _, c := range req.Contents {
			for _, p := range c.Parts {
				if p.FunctionResponse != nil {
					reply = genai.NewPartFromText("done")
				}
			}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{reply}}}, nil)
	}
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

	root, err := llmagent.New(llmagent.Config{Name: "desk", Model: bookOnce{}, Toolsets: []tool.Toolset{specs}})
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
