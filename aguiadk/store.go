package aguiadk

import (
	"context"
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
// the same resume sent again once the first has finished; two sent at once can
// still both run.
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
	}
}

// startingHistory returns the messages a run is rebuilt from.
func (b *bridge) startingHistory(ctx context.Context, userID string, in aguix.RunInput) ([]aguix.Message, error) {
	if b.store == nil || len(in.Resume) == 0 {
		return in.Messages, nil
	}
	stored, ok, err := b.store.LoadThread(ctx, userID, in.ThreadID)
	if err != nil {
		return nil, fmt.Errorf("aguiadk: loading the thread: %w", err)
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

var errNoStoredThread = errors.New("aguiadk: the thread to resume is not stored")

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
func (b *bridge) saveThread(ctx context.Context, userID, threadID string, prior []aguix.Message, run *recorder) error {
	if b.store == nil {
		return nil
	}
	messages := slices.Concat(prior, run.messages())
	if err := b.store.SaveThread(context.WithoutCancel(ctx), userID, threadID, messages); err != nil {
		return fmt.Errorf("aguiadk: saving the thread: %w", err)
	}
	return nil
}
