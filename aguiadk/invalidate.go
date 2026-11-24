package aguiadk

import (
	"context"
	"slices"
	"sync"

	"github.com/Artui/go-services/aguix"
)

// InvalidateEventName is the CUSTOM event name PublishInvalidation sends, the
// one the web component routes to its host page as an invalidation.
const InvalidateEventName = "ag_ui.invalidate"

// invalidation is the CUSTOM event's value. Reason is a pointer so that no
// reason is sent as null, as django-ag-ui sends it.
type invalidation struct {
	Keys   []string `json:"keys"`
	Reason *string  `json:"reason"`
}

// sink holds the invalidations a run's tools publish until the bridge sends
// them. ADK may run a step's calls concurrently, hence the lock.
type sink struct {
	mu     sync.Mutex
	queued []aguix.Event
}

type sinkKey struct{}

func withSink(ctx context.Context) (context.Context, *sink) {
	s := &sink{}
	return context.WithValue(ctx, sinkKey{}, s), s
}

// drain returns what was queued, in order, and empties the queue.
func (s *sink) drain() []aguix.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	queued := s.queued
	s.queued = nil
	return queued
}

// PublishInvalidation tells the page that started the run that the resources
// named by keys have changed, so it can refetch them. Call it from a spec's
// handler, or from any tool, with the context the call was given.
//
// Keys are opaque strings the host page defines; they are sent as given, and
// matched by the page, never by this package. Reason names what wrote, for the
// page's logs; empty sends null.
//
// The event is queued, not sent: the bridge sends what is queued after each
// event of the run. Within one agent the next event after a call is the one
// carrying its result, which ADK yields only once every call of the step has
// returned, so a spec's transaction has committed by then. Under a
// ParallelAgent a sibling branch's event can carry it out first, before the
// commit, and the page may refetch the old data. A call that publishes and then
// fails still sends it, which costs the page a refetch of unchanged data.
//
// It reports whether a run was listening. Outside a bridge run, or with no
// keys, nothing is queued and it returns false.
func PublishInvalidation(ctx context.Context, reason string, keys ...string) bool {
	s, _ := ctx.Value(sinkKey{}).(*sink)
	if s == nil || len(keys) == 0 {
		return false
	}
	value := invalidation{Keys: slices.Clone(keys)}
	if reason != "" {
		value.Reason = &reason
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queued = append(s.queued, aguix.Custom(InvalidateEventName, value))
	return true
}
