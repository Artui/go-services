package aguiadk

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
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
// agent answers. Nothing is kept between runs, so the client's history is the
// only state, as AG-UI intends.
//
// The run's text and tool calls stream as they arrive. Add ClientToolset to the
// agent to let it call the tools the browser declares: a run that calls one
// finishes with the call pending, and the client answers it in the next run. A
// run that ends on an assistant message has nothing to answer and finishes
// without calling the agent.
func New(root agent.Agent, opts ...Option) (aguix.Agent, error) {
	if root == nil {
		return nil, errors.New("aguiadk: New needs an agent")
	}
	b := &bridge{
		root:       root,
		newService: session.InMemoryService,
	}
	b.newRunner = func(svc session.Service) (*runner.Runner, error) {
		return runner.New(runner.Config{AppName: appName, Agent: root, SessionService: svc})
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
	optErr     error
}

func (b *bridge) Run(ctx context.Context, in aguix.RunInput, out *aguix.Stream) error {
	history, tail, err := rebuild(in.Messages, b.root.Name())
	if err != nil {
		return err
	}
	var denied map[string]bool
	if len(in.Resume) > 0 {
		history, tail, denied, err = resumed(in.Messages, in.Resume, history, tail, b.root.Name(), b.approval)
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
	r, userID, err := b.prepare(ctx, in.ThreadID, history)
	if err != nil {
		return err
	}

	tr := newTranslator(messageIDs(in.RunID))
	tr.approval, tr.denied = b.approval, denied
	runErr := b.drive(withClientTools(ctx, tools), r, userID, in.ThreadID, tail, tr, out)
	// Whatever the run opened is closed before it ends, so a failed run still
	// leaves a well-formed stream ahead of the handler's RUN_ERROR.
	if closeErr := emit(out, tr.close()); runErr == nil {
		runErr = closeErr
	}
	if runErr != nil {
		return runErr
	}
	// A gated call outranks a browser call of the same step: the client answers
	// the browser call as not finished when it resumes.
	if len(tr.interrupts) > 0 {
		return out.Finish(aguix.Interrupted(tr.interrupts...))
	}
	if pending := tr.pending(); len(pending) > 0 {
		return out.Finish(aguix.Success(pending...))
	}
	return nil
}

// prepare stores the replayed history in a session of its own and returns a
// runner over it. The session id is the thread's, so ADK and its telemetry name
// the conversation the client does.
func (b *bridge) prepare(
	ctx context.Context, threadID string, history []*session.Event,
) (*runner.Runner, string, error) {
	userID, err := b.userID(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("aguiadk: the run's user: %w", err)
	}
	if userID == "" {
		return nil, "", errors.New("aguiadk: the run names no user")
	}
	svc := b.newService()
	created, err := svc.Create(ctx, &session.CreateRequest{
		AppName: appName, UserID: userID, SessionID: threadID,
	})
	if err != nil {
		return nil, "", fmt.Errorf("aguiadk: creating the run's session: %w", err)
	}
	for _, ev := range history {
		if err := svc.AppendEvent(ctx, created.Session, ev); err != nil {
			return nil, "", fmt.Errorf("aguiadk: replaying the conversation: %w", err)
		}
	}
	r, err := b.newRunner(svc)
	if err != nil {
		return nil, "", fmt.Errorf("aguiadk: %w", err)
	}
	return r, userID, nil
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
	tail *genai.Content, tr *translator, out *aguix.Stream,
) error {
	cfg := agent.RunConfig{StreamingMode: agent.StreamingModeSSE}
	awaitingClient := false
	for ev, err := range r.Run(ctx, userID, sessionID, tail, cfg) {
		if err != nil {
			return err
		}
		frames, err := tr.translate(ev)
		if err != nil {
			return err
		}
		if err := emit(out, frames); err != nil {
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

func emit(out *aguix.Stream, frames []aguix.Event) error {
	for _, f := range frames {
		if err := out.Emit(f); err != nil {
			return err
		}
	}
	return nil
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
