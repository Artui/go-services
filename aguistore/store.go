package aguistore

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Artui/go-services/aguix"
)

// Thread is one row of a user's thread list.
type Thread struct {
	ID    string
	Title string
	// Preview is the newest message with text, collapsed to one line.
	Preview   string
	UpdatedAt time.Time

	// The run status a RunStore keeps: Status is empty when no run has
	// started, Interrupts are a waiting run's as RUN_FINISHED sent them, and
	// LastReadAt is zero until MarkRead.
	Status     string
	Reason     string
	Interrupts json.RawMessage
	LastReadAt time.Time
}

// Store keeps each user's threads. Every method is scoped to userID: a thread
// another user owns is not found.
type Store interface {
	// LoadThread returns the thread's messages, and false with no error when
	// the user has no such thread.
	LoadThread(ctx context.Context, userID, threadID string) ([]aguix.Message, bool, error)
	// SaveThread replaces the thread's messages and its preview, creating the
	// thread when the user has none by that id. A new thread is titled from its
	// first user message; a saved title is never replaced by a save.
	SaveThread(ctx context.Context, userID, threadID string, messages []aguix.Message) error
	// ListThreads returns at most limit of the user's threads, the most
	// recently saved first.
	ListThreads(ctx context.Context, userID string, limit int) ([]Thread, error)
	// RenameThread sets the thread's title. A thread the user does not have is
	// an error wrapping services.ErrNotFound.
	RenameThread(ctx context.Context, userID, threadID, title string) error
	// DeleteThread removes the thread; one the user does not have is no error.
	DeleteThread(ctx context.Context, userID, threadID string) error
}

// Option configures a store.
type Option func(*config)

type config struct {
	now     func() time.Time
	dialect Dialect
}

// WithClock sets the clock a store stamps each save with; the default is
// time.Now.
func WithClock(now func() time.Time) Option {
	return func(c *config) { c.now = now }
}

func configure(opts []Option) config {
	c := config{now: time.Now}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

const (
	titleLimit   = 60
	previewLimit = 120
	defaultTitle = "New conversation"
)

// titleFrom is the first user message with text, as django-spec-agents titles
// a thread.
func titleFrom(messages []aguix.Message) string {
	for _, m := range messages {
		if m.Role == "user" {
			if text := clean(m); text != "" {
				return truncate(text, titleLimit)
			}
		}
	}
	return defaultTitle
}

// previewFrom is the newest message with text.
func previewFrom(messages []aguix.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if text := clean(messages[i]); text != "" {
			return truncate(text, previewLimit)
		}
	}
	return ""
}

// clean is a message's string content with its whitespace collapsed; content
// in parts or an object has none, as the client reads it.
func clean(m aguix.Message) string {
	if len(m.Content) == 0 || m.Content[0] != '"' {
		return ""
	}
	return strings.Join(strings.Fields(m.Text()), " ")
}

func truncate(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return strings.TrimRight(string([]rune(text)[:limit-1]), " ") + "…"
}
