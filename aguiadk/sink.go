package aguiadk

import (
	"context"
	"slices"
	"sync"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/platform"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/tool"
)

// sink holds the frames a run's tools and callbacks publish until the bridge
// sends them.
//
// A frame published inside a tool call is held under that call's id and sent
// after the event that carries the call's result, so a spec's transaction has
// committed by the time the page hears of it, whatever a sibling branch of a
// ParallelAgent yields meanwhile. A frame published anywhere else, a model
// callback for instance, has no result to wait for and goes out ahead of the
// next event, which ADK yields after the callback ran. A call the plugin could
// not name (ADK runs a streaming tool without the tool callbacks) has no id to
// wait on, so its frames go out after the next event, which is its result's.
//
// ADK may run a step's calls concurrently, hence the lock.
type sink struct {
	mu       sync.Mutex
	loose    []aguix.Event
	trailing []aguix.Event
	byCall   map[string][]aguix.Event
	order    []string

	// live sends a frame now, for what is only worth seeing while it happens.
	live func(aguix.Event)
}

type sinkKey struct{}

func withSink(ctx context.Context) (context.Context, *sink) {
	s := &sink{byCall: map[string][]aguix.Event{}, live: func(aguix.Event) {}}
	ctx = context.WithValue(ctx, sinkKey{}, s)
	return platform.WithTaskRunner(ctx, s.perCall), s
}

func sinkFrom(ctx context.Context) *sink {
	s, _ := ctx.Value(sinkKey{}).(*sink)
	return s
}

// publish queues ev under the call ctx belongs to, if any.
func (s *sink) publish(ctx context.Context, ev aguix.Event) {
	c := slotOf(ctx)
	id := c.root()
	s.mu.Lock()
	defer s.mu.Unlock()
	if c == nil {
		s.loose = append(s.loose, ev)
		return
	}
	if id == "" {
		s.trailing = append(s.trailing, ev)
		return
	}
	if _, held := s.byCall[id]; !held {
		s.order = append(s.order, id)
	}
	s.byCall[id] = append(s.byCall[id], ev)
}

// drain returns what goes out around the frames of the next event: before
// them, everything published outside a call, which happened before the event
// was yielded; after them, what unnamed calls published and then what each
// call the event answers published, in the order it answers them.
func (s *sink) drain(answered []string) (before, after []aguix.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before, after = s.loose, s.trailing
	s.loose, s.trailing = nil, nil
	for _, id := range answered {
		after = append(after, s.byCall[id]...)
		s.release(id)
	}
	return before, after
}

// drainAll returns everything still held, for a run that is ending. A call
// whose result never reaches the client (one left waiting for a person, or cut
// off by a failure) still tells the page about a write that may have committed.
func (s *sink) drainAll() []aguix.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append(s.loose, s.trailing...)
	s.loose, s.trailing = nil, nil
	for _, id := range slices.Clone(s.order) {
		out = append(out, s.byCall[id]...)
		s.release(id)
	}
	return out
}

func (s *sink) release(id string) {
	delete(s.byCall, id)
	s.order = slices.DeleteFunc(s.order, func(held string) bool { return held == id })
}

// slot is one tool call's place in the run, planted under the context ADK gives
// the call, so that a context the call derives (a spec's transaction, say)
// still finds it. parent is the call running the agent this call belongs to,
// when that agent was delegated to through agenttool.
type slot struct {
	parent *slot
	sink   *sink

	mu       sync.Mutex
	id, name string
	reported bool
}

type slotKey struct{}

func slotOf(ctx context.Context) *slot {
	c, _ := ctx.Value(slotKey{}).(*slot)
	return c
}

func (c *slot) named() (id, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.id, c.name
}

// root is the id of the outermost call: the one whose result the client sees.
// A call made inside a delegation answers only the sub-agent, and the client
// hears of it through the delegating call's result.
func (c *slot) root() string {
	id := ""
	for ; c != nil; c = c.parent {
		if own, _ := c.named(); own != "" {
			id = own
		}
	}
	return id
}

// perCall is the task runner ADK fans a step's calls out through. It runs them
// concurrently, as ADK's own runner does, and gives each a context of its own
// carrying a call that the plugin then names. It replaces a runner a caller
// installed with platform.WithTaskRunner on the request's context.
func (s *sink) perCall(ctx context.Context, tasks []func(context.Context)) {
	parent := slotOf(ctx)
	var wg sync.WaitGroup
	for _, task := range tasks {
		wg.Go(func() { task(context.WithValue(ctx, slotKey{}, &slot{parent: parent, sink: s})) })
	}
	wg.Wait()
}

// sinkPlugin names each call before any callback or the tool itself runs, and
// reports a delegated sub-agent's calls as they happen. agenttool's runner
// inherits it, so it sees inside delegations too.
func sinkPlugin() *plugin.Plugin {
	p, _ := plugin.New(plugin.Config{
		Name: "aguiadk",
		BeforeToolCallback: func(ctx agent.Context, t tool.Tool, _ map[string]any) (map[string]any, error) {
			c := slotOf(ctx)
			if c == nil {
				return nil, nil
			}
			c.mu.Lock()
			c.id, c.name = ctx.FunctionCallID(), t.Name()
			c.mu.Unlock()
			progress(ctx, c, phaseToolCall, nil)
			return nil, nil
		},
		// A call to a tool the agent lacks reaches only the error callback; one
		// that fails reaches it first, so the after callback never reports an
		// error, and a call is reported once.
		OnToolErrorCallback: func(ctx agent.Context, _ tool.Tool, _ map[string]any, _ error) (map[string]any, error) {
			returned(ctx, false)
			return nil, nil
		},
		AfterToolCallback: func(
			ctx agent.Context, _ tool.Tool, _, result map[string]any, _ error,
		) (map[string]any, error) {
			returned(ctx, !failed(result))
			return nil, nil
		},
	})
	return p
}

// returned reports the end of ctx's call, once.
func returned(ctx agent.Context, ok bool) {
	c := slotOf(ctx)
	if c == nil {
		return
	}
	c.mu.Lock()
	again := c.reported
	c.reported = true
	c.mu.Unlock()
	if !again {
		progress(ctx, c, phaseToolResult, &ok)
	}
}
