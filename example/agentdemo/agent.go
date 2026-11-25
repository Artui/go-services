// Command agentdemo serves the library registry to the real <ag-ui-chat> web
// component, answered by Claude.
//
// It exists because the rest of this repository can only prove that aguix emits
// the frames the protocol describes. Whether a client renders them is a
// different question, and the only honest way to answer it is to point a real
// client at a real server. The component is loaded from a CDN at a pinned
// version, so the page has no build step.
//
// The path is the one a consumer would take: the registry becomes ADK tools
// through adkx.Toolset, the model is ADK's own anthropicmodel, and aguiadk turns
// one AG-UI run into one ADK invocation. Everything under the tools -- validation,
// permissions, the transaction boundary -- is the kernel the HTTP and MCP
// adapters run, against a real SQLite database.
//
// It needs ANTHROPIC_API_KEY. The test beside it drives the same handler with a
// scripted model, so CI proves the wiring without a key.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/Artui/go-services/adkx"
	"github.com/Artui/go-services/aguiadk"
	"github.com/Artui/go-services/aguix"
	"github.com/Artui/go-services/example"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/anthropicmodel"
	"google.golang.org/adk/v2/tool"
)

const modelName = "claude-sonnet-5-5"

const instruction = `You are the librarian of a small lending library. Use the tools to look up
books and loans and to borrow a book for the member you are talking to. Report what the tool
returned, in a sentence or two; when a tool refuses, say why and do not retry the same call.
Fines are reported in cents. Ask the catalogue when a question needs several lookups. Draw
chart_loans when the member asks how popular the books are. After you answer, you may offer
follow-ups with suggest_follow_ups.`

// memberKey carries the signed-in member through the request context.
//
// Identity arrives the way it does in any net/http server -- put there by
// middleware -- because aguix has no channel of its own for it and inventing
// one would compete with the one an application already has.
type memberKey struct{}

func withMember(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A demo signs in as Ada. A real deployment reads a session here.
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), memberKey{}, int64(1))))
	})
}

// userID is what the bridge hands ADK as the invocation's user.
func userID(ctx context.Context) (string, error) {
	id, ok := ctx.Value(memberKey{}).(int64)
	if !ok {
		return "", errors.New("this run is not signed in")
	}
	return strconv.FormatInt(id, 10), nil
}

// principal turns the invocation's user back into what the registry's resolver
// expects: the member's id.
func principal(ctx agent.Context) (any, error) {
	return strconv.ParseInt(ctx.UserID(), 10, 64)
}

// newHandler serves llm over AG-UI with the registry's specs as its tools.
func newHandler(llm model.LLM, db *sql.DB) (http.Handler, error) {
	specs, err := adkx.Toolset(example.Registry(db), principal)
	if err != nil {
		return nil, fmt.Errorf("toolset: %w", err)
	}
	tools, err := pageTools(llm, specs, db)
	if err != nil {
		return nil, err
	}
	root, err := llmagent.New(llmagent.Config{
		Name: "librarian", Model: llm, Instruction: instruction,
		Tools: tools, Toolsets: []tool.Toolset{specs, aguiadk.ClientToolset()},
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{compact},
	})
	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}
	bridge, err := aguiadk.New(root, aguiadk.WithUserID(userID))
	if err != nil {
		return nil, err
	}
	handler, err := aguix.Handler(bridge, aguix.WithOnError(
		func(_ *http.Request, err error) { log.Printf("run failed: %v", err) }))
	if err != nil {
		return nil, fmt.Errorf("handler: %w", err)
	}
	return withMember(handler), nil
}

func main() {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		log.Fatal("agentdemo needs ANTHROPIC_API_KEY: the model answers, the tools are the library's")
	}
	ctx := context.Background()

	db, err := example.Open(ctx, "agentdemo")
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := example.Seed(ctx, db); err != nil {
		log.Fatalf("seed: %v", err)
	}

	llm, err := anthropicmodel.NewModel(ctx, modelName, anthropicmodel.ClientConfig{
		Backend: anthropicmodel.BackendAnthropic, APIKey: key,
		// Named, or ANTHROPIC_BASE_URL could redirect the key; see adkx's doc.
		BaseURL: "https://api.anthropic.com",
	})
	if err != nil {
		log.Fatalf("model: %v", err)
	}
	handler, err := newHandler(llm, db)
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("POST /agent", handler)
	mux.HandleFunc("GET /", page)

	address := "localhost:" + port()
	log.Printf("agentdemo on http://%s", address)
	server := &http.Server{Addr: address, Handler: mux}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func port() string {
	if fromEnv := os.Getenv("PORT"); fromEnv != "" {
		return fromEnv
	}
	return "8099"
}

func page(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(html))
}
