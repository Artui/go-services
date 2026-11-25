package aguiadk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/Artui/go-services/aguix"
)

// ThreadStore keeps each user's threads between runs. The aguistore module
// implements it in memory and over database/sql; any type with these two
// methods will do.
//
// Both are keyed by the run's user, the id WithUserID resolves, and the thread
// id: a store must never answer one user with another's thread.
type ThreadStore interface {
	// LoadThread returns the thread's messages, and false with no error when
	// the user has no such thread.
	LoadThread(ctx context.Context, userID, threadID string) ([]aguix.Message, bool, error)
	// SaveThread replaces the thread's messages, creating the thread when the
	// user has none by that id.
	SaveThread(ctx context.Context, userID, threadID string, messages []aguix.Message) error
}

// RunStore is a ThreadStore that also records each thread's run status, so a
// thread list can say which runs are going, failed, or parked on an approval,
// and a parked approval is answered once. The aguistore module's stores are
// one; WithThreadStore uses a store as one when it has these methods.
//
// The statuses are "idle", "running", "waiting" and "failed".
type RunStore interface {
	// StartRun marks the thread running under runID. With resumes, the
	// interrupt ids the run answers, it is a claim that starts only while the
	// thread waits on every one of them, and it reports false when it is lost.
	StartRun(ctx context.Context, userID, threadID, runID string, resumes []string) (bool, error)
	// FinishRun records how runID ended: its status, why it failed, and the
	// interrupts it parked on, the bytes RUN_FINISHED carries.
	FinishRun(ctx context.Context, userID, threadID, runID, status, reason string, interrupts json.RawMessage) error
}

// WithThreadStore saves each run's conversation to store when the run ends, so
// a thread outlives the page and the process, and reads it back to resume a
// parked approval.
//
// Which history a run starts from follows django-ag-ui. An ordinary run starts
// from the messages the client sends, so retry and fork keep working, and the
// thread is saved as those messages followed by what the run sent. A run that
// resumes interrupts starts from the stored thread instead, followed by the
// client's trailing tool messages that answer a browser tool's call the stored
// thread left open, so a resume keeps to the calls and arguments the thread
// holds (an approver's edited arguments aside). A resume of a thread the store
// does not hold is refused, as django-ag-ui refuses it.
//
// The message ids a resume names then come from the store, which also refuses
// the same resume sent again once the first has finished. A store that is a
// RunStore also records each run's status, and claims a resume before it runs,
// so of two sent at once one is refused and the call runs once; with a store
// that is not, two sent at once can still both run.
//
// Every run loads the stored thread: the client sends its history without
// activities, so a run puts each stored activity back after the message it
// followed, and a snapshot of a stored activity replaces it in place.
//
// The thread is saved when the run finishes, waiting on the browser or on an
// approval included, and not when it fails or has nothing to answer. The save
// does not stop when the request is cancelled, so a run that reached its end
// is kept, and a save that fails fails the run with a RUN_ERROR.
func WithThreadStore(store ThreadStore) Option {
	return func(b *bridge) {
		if store == nil {
			b.optErr = errors.New("aguiadk: WithThreadStore needs a store")
			return
		}
		b.store = store
		b.runs, _ = store.(RunStore)
	}
}

// startingHistory returns the messages a run is rebuilt from.
func (b *bridge) startingHistory(ctx context.Context, userID string, in aguix.RunInput) ([]aguix.Message, error) {
	if b.store == nil {
		return in.Messages, nil
	}
	stored, ok, err := b.store.LoadThread(ctx, userID, in.ThreadID)
	if err != nil {
		return nil, fmt.Errorf("aguiadk: loading the thread: %w", err)
	}
	if len(in.Resume) == 0 {
		return withActivities(stored, in.Messages), nil
	}
	if !ok {
		return nil, errNoStoredThread
	}
	browser := map[string]bool{}
	for _, t := range in.Tools {
		browser[t.Name] = true
	}
	return slices.Concat(stored, openAnswers(stored, in.Messages, browser)), nil
}

// withActivities puts the stored thread's activities back into the client's
// messages, each after the message it followed, since the client sends none.
func withActivities(stored, client []aguix.Message) []aguix.Message {
	sent := map[string]bool{}
	for _, m := range client {
		sent[m.ID] = true
	}
	after := map[string][]aguix.Message{}
	anchor := ""
	for _, m := range stored {
		switch {
		case m.Role != "activity":
			anchor = m.ID
		case !sent[m.ID]:
			after[anchor] = append(after[anchor], m)
		}
	}
	if len(after) == 0 {
		return client
	}
	out := slices.Clone(after[""])
	for _, m := range client {
		out = append(append(out, m), after[m.ID]...)
	}
	return out
}

var (
	errNoStoredThread = errors.New("aguiadk: the thread to resume is not stored")
	errResumeTaken    = errors.New("aguiadk: the interrupts this run answers are not waiting; another run answered them")
)

// startRun records the run as running, claiming the interrupts it answers.
func (b *bridge) startRun(ctx context.Context, userID string, in aguix.RunInput) error {
	if b.runs == nil {
		return nil
	}
	var resumes []string
	for _, entry := range in.Resume {
		resumes = append(resumes, entry.InterruptID)
	}
	won, err := b.runs.StartRun(ctx, userID, in.ThreadID, in.RunID, resumes)
	if err != nil {
		return fmt.Errorf("aguiadk: recording the run's start: %w", err)
	}
	if !won {
		return errResumeTaken
	}
	return nil
}

// endRun records how the run ended and returns its error, a failure to record
// a run that succeeded among them. A cancelled run leaves the thread idle.
func (b *bridge) endRun(ctx context.Context, userID string, in aguix.RunInput, interrupts []aguix.Interrupt, runErr error) error {
	if b.runs == nil {
		return runErr
	}
	status, reason := "idle", ""
	var parked json.RawMessage
	switch {
	case runErr != nil && ctx.Err() == nil:
		status, reason = "failed", "error"
	case runErr == nil && len(interrupts) > 0:
		// Stream.Finish has accepted these, so they encode, as RUN_FINISHED does.
		status = "waiting"
		parked, _ = json.Marshal(interrupts)
	}
	err := b.runs.FinishRun(context.WithoutCancel(ctx), userID, in.ThreadID, in.RunID, status, reason, parked)
	if runErr == nil && err != nil {
		return fmt.Errorf("aguiadk: recording the run's end: %w", err)
	}
	return runErr
}

// openAnswers returns the client's trailing tool messages that answer a call
// to a browser tool stored left open, each once.
func openAnswers(stored, client []aguix.Message, browser map[string]bool) []aguix.Message {
	open := map[string]bool{}
	for _, m := range stored {
		for _, c := range m.ToolCalls {
			open[c.ID] = browser[c.Function.Name]
		}
		if m.Role == "tool" {
			delete(open, m.ToolCallID)
		}
	}
	start := len(client)
	for start > 0 && client[start-1].Role == "tool" {
		start--
	}
	var out []aguix.Message
	for _, m := range client[start:] {
		if open[m.ToolCallID] {
			delete(open, m.ToolCallID)
			out = append(out, m)
		}
	}
	return out
}

// saveThread stores the run's starting history followed by what it sent.
func (b *bridge) saveThread(ctx context.Context, userID, threadID string, run *recorder) error {
	if b.store == nil {
		return nil
	}
	messages := slices.Concat(run.prior, run.messages())
	if err := b.store.SaveThread(context.WithoutCancel(ctx), userID, threadID, messages); err != nil {
		return fmt.Errorf("aguiadk: saving the thread: %w", err)
	}
	return nil
}
