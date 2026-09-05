// Command agentdemo serves the library registry as an AG-UI agent, with a page
// that drives it through the real <ag-ui-chat> web component.
//
// It exists because the rest of this repository can only prove that aguix emits
// the frames the protocol describes. Whether a client actually renders them is
// a different question, and the only honest way to answer it is to point a real
// client at a real server. The component is loaded from a CDN at a pinned
// version, so this demo has no build step and no dependency on a checkout
// sitting next to it.
//
// The agent is scripted. There is no model here and no API key: the rules below
// decide what to call, and everything under them -- validation, permissions,
// the transaction boundary -- is the same kernel the HTTP and MCP adapters run.
// That is the point. A demo that needed a model would be testing the model.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/aguix"
	"github.com/Artui/go-services/example"
)

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

func member(ctx context.Context) (any, error) {
	id, ok := ctx.Value(memberKey{}).(int64)
	if !ok {
		return nil, fmt.Errorf("%w: this run is not signed in", services.ErrPermission)
	}
	return id, nil
}

func main() {
	ctx := context.Background()

	db, err := example.Open(ctx, "agentdemo")
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := example.Seed(ctx, db); err != nil {
		log.Fatalf("seed: %v", err)
	}

	toolbox, err := aguix.NewToolbox(example.Registry(db), member)
	if err != nil {
		log.Fatalf("toolbox: %v", err)
	}

	// The same script the module's own test drives, so the browser and CI
	// are not looking at different agents.
	agent := example.Librarian(toolbox)

	handler, err := aguix.Handler(agent, aguix.WithOnError(
		func(_ *http.Request, err error) { log.Printf("run failed: %v", err) }))
	if err != nil {
		log.Fatalf("handler: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("POST /agent", withMember(handler))
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
	_, _ = strings.NewReader(html).WriteTo(w)
}
