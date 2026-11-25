package aguiadk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// Anonymous is the explicit choice for an unauthenticated mount: every run
// belongs to the one user "anonymous", so a toolset that names its principal by
// user id sees every caller as the same one. Pass it to WithUserID only where
// the endpoint is open by design.
func Anonymous(context.Context) (string, error) { return "anonymous", nil }

// appName names the per-run session's application. The session lives for one
// run in a store of its own, so the name only has to be non-empty.
const appName = "aguiadk"

// Option configures the agent New returns.
type Option func(*bridge)

// WithUserID sets the ADK user id of a run from the request's context, which
// carries whatever the server's authentication put there.
//
// New requires it, and refuses a nil function: a mount that forgot it would
// merge every caller into one identity for tools that read the invocation's
// user, adkx.UserID among them. An open endpoint says so with Anonymous. An
// error, or an empty id, fails the run before the agent starts.
func WithUserID(fn func(ctx context.Context) (string, error)) Option {
	return func(b *bridge) { b.userID = fn }
}

// New serves root over AG-UI. The result is what aguix.Handler takes. It needs
// WithUserID.
//
// Root should be an LLM agent. The model turns of the client's history are
// replayed as authored by root, so a sub-agent that made a browser call resumes
// at the root on the next run, and the children of a workflow root see those
// turns as another agent's context.
//
// Every run starts from the conversation the client sends: it is replayed into
// a fresh in-memory session, and the trailing user or tool message is what the
// agent answers. The client's state, a JSON object, seeds that session's state;
// a non-empty one is echoed as a STATE_SNAPSHOT, and each write the run makes
// streams as a STATE_DELTA. Nothing is kept between runs, so the client's
// history and state are the only state, as AG-UI intends.
//
// The run's thinking, text and tool calls stream as they arrive. Add
// ClientToolset to the agent to let it call the tools the browser declares: a
// run that calls one finishes with the call pending, and the client answers it
// in the next run. A run that ends on an assistant message has nothing to
// answer and finishes without calling the agent.
//
// Each run installs its own ADK task runner (platform.WithTaskRunner) to tell a
// step's concurrent calls apart, replacing one a caller put on the request's
// context; ADK has no way to read the installed one back and wrap it.
func New(root agent.Agent, opts ...Option) (aguix.Agent, error) {
	if root == nil {
		return nil, errors.New("aguiadk: New needs an agent")
	}
	b := &bridge{
		root:       root,
		newService: session.InMemoryService,
	}
	plugins := runner.PluginConfig{Plugins: []*plugin.Plugin{sinkPlugin(), instructionsPlugin()}}
	b.newRunner = func(svc session.Service) (*runner.Runner, error) {
		return runner.New(runner.Config{
			AppName: appName, Agent: root, SessionService: svc, MemoryService: b.memory, PluginConfig: plugins,
		})
	}
	for _, opt := range opts {
		opt(b)
	}
	if b.optErr != nil {
		return nil, b.optErr
	}
	if b.userID == nil {
		return nil, errors.New(
			"aguiadk: New needs WithUserID; pass aguiadk.Anonymous for an unauthenticated mount")
	}
	// The agent tree is checked once here, so a malformed one fails at start-up
	// rather than on every run.
	if _, err := b.newRunner(b.newService()); err != nil {
		return nil, fmt.Errorf("aguiadk: %w", err)
	}
	return b, nil
}

type bridge struct {
	root       agent.Agent
	userID     func(context.Context) (string, error)
	newService func() session.Service
	newRunner  func(session.Service) (*runner.Runner, error)
	approval   *Approval
	store      ThreadStore
	runs       RunStore
	memory     memory.Service
	optErr     error

	instructions runInstructions

	maxLLMCalls int
}

func (b *bridge) Run(ctx context.Context, in aguix.RunInput, out *aguix.Stream) error {
	seed, err := seedState(in.State)
	if err != nil {
		return err
	}
	userID, err := b.user(ctx)
	if err != nil {
		return err
	}
	messages, err := b.startingHistory(ctx, userID, in)
	if err != nil {
		return err
	}
	history, tail, err := rebuild(messages, b.root.Name())
	if err != nil {
		return err
	}
	var denied map[string]bool
	if len(in.Resume) > 0 {
		history, tail, denied, err = resumed(messages, in.Resume, history, tail, b.root.Name(), b.approval)
		if err != nil {
			return err
		}
	}
	// ADK would still call the model, on a conversation whose last turn is its
	// own; there is nothing to answer, so the run ends here.
	if tail == nil {
		return nil
	}
	tools, err := clientTools(in.Tools)
	if err != nil {
		return err
	}
	if err := b.startRun(ctx, userID, in); err != nil {
		return err
	}
	interrupts, err := b.serve(ctx, in, out, userID, messages, history, tail, tools, denied, seed)
	return b.endRun(ctx, userID, in, interrupts, err)
}

// serve runs the agent on the rebuilt conversation and returns the interrupts
// it finished on.
func (b *bridge) serve(
	ctx context.Context, in aguix.RunInput, out *aguix.Stream, userID string, messages []aguix.Message,
	history []*session.Event, tail *genai.Content, tools []*clientTool, denied map[string]bool, seed map[string]any,
) ([]aguix.Interrupt, error) {
	r, svc, err := b.prepare(ctx, userID, in.ThreadID, history, seed)
	if err != nil {
		return nil, err
	}
	if len(seed) > 0 {
		if err := out.Emit(aguix.StateSnapshot(seed)); err != nil {
			return nil, err
		}
	}

	tr := newTranslator(messageIDs(in.RunID))
	tr.approval, tr.denied, tr.based = b.approval, denied, seed != nil
	ctx, queued := withSink(withClientTools(withInstructions(ctx, b.instructions.of(in)), tools))
	tr.sink = queued
	w := &wire{out: out, rec: &recorder{prior: slices.Clone(messages)}}
	queued.live = w.live
	runErr := b.drive(ctx, r, userID, in.ThreadID, tail, tr, queued, w)
	// Whatever the run opened is closed before it ends, so a failed run still
	// leaves a well-formed stream ahead of the handler's RUN_ERROR, and still
	// tells the page about a write that committed before the failure.
	if closeErr := w.end(append(tr.close(), queued.drainAll()...)); runErr == nil {
		runErr = closeErr
	}
	if runErr != nil {
		return nil, runErr
	}
	if err := b.saveThread(ctx, userID, in.ThreadID, w.rec); err != nil {
		return nil, err
	}
	// A gated call outranks a browser call of the same step: the client answers
	// the browser call as not finished when it resumes.
	if len(tr.interrupts) > 0 {
		return tr.interrupts, out.Finish(aguix.Interrupted(tr.interrupts...))
	}
	if pending := tr.pending(); len(pending) > 0 {
		return nil, out.Finish(aguix.Success(pending...))
	}
	return nil, b.remember(ctx, svc, userID, in.ThreadID)
}

// seedState decodes the client's state into the session's initial state, keeping
// numbers as json.Number. Absent or null is no state and returns nil. A "temp:"
// key is dropped: it belongs to one invocation, and the run's writes never send one.
func seedState(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	state, err := decodeObject(string(raw))
	if err != nil {
		return nil, fmt.Errorf("aguiadk: the run's state is not a JSON object: %w", err)
	}
	maps.DeleteFunc(state, func(key string, _ any) bool { return strings.HasPrefix(key, session.KeyPrefixTemp) })
	return state, nil
}

// user resolves the run's user, which owns its session and its stored thread.
func (b *bridge) user(ctx context.Context) (string, error) {
	userID, err := b.userID(ctx)
	if err != nil {
		return "", fmt.Errorf("aguiadk: the run's user: %w", err)
	}
	if userID == "" {
		return "", errors.New("aguiadk: the run names no user")
	}
	return userID, nil
}

// prepare stores the replayed history in a session of its own, seeded with the
// client's state, and returns a runner over it and the session's service. The
// session id is the thread's, so ADK and its telemetry name the conversation
// the client does.
func (b *bridge) prepare(
	ctx context.Context, userID, threadID string, history []*session.Event, state map[string]any,
) (*runner.Runner, session.Service, error) {
	svc := b.newService()
	created, err := svc.Create(ctx, &session.CreateRequest{
		AppName: appName, UserID: userID, SessionID: threadID, State: state,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("aguiadk: creating the run's session: %w", err)
	}
	for _, ev := range history {
		if err := svc.AppendEvent(ctx, created.Session, ev); err != nil {
			return nil, nil, fmt.Errorf("aguiadk: replaying the conversation: %w", err)
		}
	}
	r, err := b.newRunner(svc)
	if err != nil {
		return nil, nil, fmt.Errorf("aguiadk: %w", err)
	}
	return r, svc, nil
}

// drive runs the agent and streams what it yields.
//
// A step that calls a client tool beside a server tool is where it stops on its
// own: ADK runs the server tool, yields its response, and would then call the
// model again with the client's call still unanswered. So once a step has
// named a long-running call, the run ends after the event carrying that step's
// responses, unless that event says a call is waiting for confirmation: the
// request follows it, and becomes the run's interrupt.
func (b *bridge) drive(
	ctx context.Context, r *runner.Runner, userID, sessionID string,
	tail *genai.Content, tr *translator, queued *sink, w *wire,
) error {
	cfg := agent.RunConfig{StreamingMode: agent.StreamingModeSSE, MaxLLMCalls: b.maxLLMCalls}
	awaitingClient := false
	var used tally
	for ev, err := range r.Run(ctx, userID, sessionID, tail, cfg) {
		if err != nil {
			return err
		}
		// The whole sum is set after each call, so a run the handler cancels on
		// its timeout, or one that stops for the client, still reports it.
		if used.add(ev) {
			if err := w.out.SetUsage(used.entries...); err != nil {
				return err
			}
		}
		frames, err := tr.translate(ev)
		if err != nil {
			return err
		}
		// What a call published goes after the event carrying its result.
		before, after := queued.drain(answered(ev))
		if err := w.emit(slices.Concat(before, frames, after)); err != nil {
			return err
		}
		if ev.Partial {
			continue
		}
		if len(ev.LongRunningToolIDs) > 0 {
			awaitingClient = true
		} else if awaitingClient && hasResponses(ev) && len(ev.Actions.RequestedToolConfirmations) == 0 {
			return nil
		}
	}
	return nil
}

func hasResponses(ev *session.Event) bool {
	return ev.Content != nil && slices.ContainsFunc(ev.Content.Parts, func(p *genai.Part) bool {
		return p != nil && p.FunctionResponse != nil
	})
}

// answered names the calls a complete event carries responses for. A partial
// event carries none ADK acts on.
func answered(ev *session.Event) []string {
	if ev.Partial || ev.Content == nil {
		return nil
	}
	var ids []string
	for _, p := range ev.Content.Parts {
		if p != nil && p.FunctionResponse != nil {
			ids = append(ids, p.FunctionResponse.ID)
		}
	}
	return ids
}

// wire is the run's stream as the bridge and a running call share it: a
// delegated sub-agent's progress goes out from the call's goroutine, between
// the bridge's own frames, never inside a batch of them. Once the run has
// ended, what a call still running in an abandoned branch sends is dropped.
type wire struct {
	mu   sync.Mutex
	out  *aguix.Stream
	rec  *recorder
	done bool
}

func (w *wire) emit(frames []aguix.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.send(frames)
}

func (w *wire) send(frames []aguix.Event) error {
	for _, f := range frames {
		if err := w.out.Emit(f); err != nil {
			return err
		}
		w.rec.observe(f)
	}
	return nil
}

// live sends one frame now. A write that fails fails the bridge's next one too,
// which is where the run reports it.
func (w *wire) live(f aguix.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.done {
		_ = w.send([]aguix.Event{f})
	}
}

// end sends the run's last frames and closes the wire to live ones.
func (w *wire) end(frames []aguix.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done = true
	return w.send(frames)
}

// messageIDs numbers a run's messages under its run id. The client makes run ids
// unique, so the ids never repeat across a thread, and a recorded run replays
// with the same ones.
func messageIDs(runID string) func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("%s-%d", runID, n)
	}
}

// WithMaxLLMCalls bounds how many times one run may call the model: it is ADK's
// agent.RunConfig.MaxLLMCalls, with ADK's meanings. Zero is ADK's default, 500
// or the ADK_MAX_LLM_CALLS environment variable, and a negative value is no
// limit. A run that would pass it fails with an error wrapping
// agent.ErrLLMCallsLimitExceeded, which the handler sends as a RUN_ERROR.
func WithMaxLLMCalls(n int) Option {
	return func(b *bridge) { b.maxLLMCalls = n }
}
