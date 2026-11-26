package aguiadk

import (
	"context"
	"sync/atomic"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/tool"
)

// toolReach notes whether a run reached any tool; a resume runs only the calls
// it answers, so after one the claim cannot go back without running it twice.
type toolReach struct{ hit atomic.Bool }

type toolReachKey struct{}

func watchTools(ctx context.Context) (context.Context, *toolReach) {
	w := &toolReach{}
	return context.WithValue(ctx, toolReachKey{}, w), w
}

func (w *toolReach) reached() bool { return w.hit.Load() }

func toolReachPlugin() *plugin.Plugin {
	p, _ := plugin.New(plugin.Config{
		Name: "aguiadk-tool-reach",
		BeforeToolCallback: func(ctx agent.Context, _ tool.Tool, _ map[string]any) (map[string]any, error) {
			if w, _ := ctx.Value(toolReachKey{}).(*toolReach); w != nil {
				w.hit.Store(true)
			}
			return nil, nil
		},
	})
	return p
}
