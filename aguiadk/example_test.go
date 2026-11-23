package aguiadk_test

import (
	"context"
	"log"
	"net/http"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"github.com/Artui/go-services/aguiadk"
	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
)

type signedIn struct{}

// The server's specs and the browser's tools side by side, served at /agent.
func Example() {
	var llm model.LLM // a provider model, such as anthropicmodel's

	reg := services.New(func(_ context.Context, who any) (string, error) {
		user, _ := who.(string)
		return user, nil
	})
	specs, err := adkx.Toolset(reg, adkx.UserID)
	if err != nil {
		log.Fatal(err)
	}
	root, err := llmagent.New(llmagent.Config{
		Name: "assistant", Model: llm,
		Toolsets: []tool.Toolset{specs, aguiadk.ClientToolset()},
	})
	if err != nil {
		log.Fatal(err)
	}
	bridge, err := aguiadk.New(root, aguiadk.WithUserID(func(ctx context.Context) (string, error) {
		user, _ := ctx.Value(signedIn{}).(string) // set by the server's authentication
		return user, nil
	}))
	if err != nil {
		log.Fatal(err)
	}
	handler, err := aguix.Handler(bridge)
	if err != nil {
		log.Fatal(err)
	}
	http.Handle("/agent", handler)
}
