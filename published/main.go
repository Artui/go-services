// Command published is what a consumer who installs only tags builds: no
// replace, no workspace, no checkout beside it. CI builds it with GOWORK=off.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"github.com/Artui/go-services/aguiadk"
	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/anthropicmodel"
	"google.golang.org/adk/v2/tool"
)

type echoIn struct {
	Text string `json:"text"`
}

type echoOut struct {
	Said string `json:"said"`
}

func registry() *services.Registry[struct{}] {
	reg := services.New(func(context.Context, any) (struct{}, error) { return struct{}{}, nil })
	services.MustRegister(reg, services.Spec[struct{}, echoIn, echoOut]{
		Name: "echo", Description: "Repeat the text back.", Kind: services.Query,
		Run: func(_ services.Ctx[struct{}], in echoIn) (echoOut, error) {
			return echoOut{Said: in.Text}, nil
		},
	})
	return reg
}

// handler serves llm over AG-UI with the registry's specs and the browser's tools.
func handler(llm model.LLM) (http.Handler, error) {
	specs, err := adkx.Toolset(registry(), adkx.Anonymous)
	if err != nil {
		return nil, err
	}
	root, err := llmagent.New(llmagent.Config{
		Name: "assistant", Model: llm,
		Toolsets: []tool.Toolset{specs, aguiadk.ClientToolset()},
	})
	if err != nil {
		return nil, err
	}
	bridge, err := aguiadk.New(root, aguiadk.WithUserID(aguiadk.Anonymous))
	if err != nil {
		return nil, err
	}
	return aguix.Handler(bridge)
}

func main() {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		log.Fatal(errors.New("set ANTHROPIC_API_KEY"))
	}
	llm, err := anthropicmodel.NewModel(context.Background(), "claude-sonnet-5-5",
		anthropicmodel.ClientConfig{
			Backend: anthropicmodel.BackendAnthropic, APIKey: key,
			// Named, or ANTHROPIC_BASE_URL could redirect the key; see adkx's doc.
			BaseURL: "https://api.anthropic.com",
		})
	if err != nil {
		log.Fatal(err)
	}
	h, err := handler(llm)
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("POST /agent", h)
	log.Fatal(http.ListenAndServe("localhost:8098", mux))
}
