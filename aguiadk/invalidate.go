package aguiadk

import (
	"context"
	"slices"

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

// PublishInvalidation tells the page that started the run that the resources
// named by keys have changed, so it can refetch them. Call it from a spec's
// handler, or from any tool, with the context the call was given or one
// derived from it.
//
// Keys are opaque strings the host page defines; they are sent as given, and
// matched by the page, never by this package. Reason names what wrote, for the
// page's logs; empty sends null.
//
// The event is queued, not sent. One published inside a tool call goes out
// right after that call's TOOL_CALL_RESULT, which ADK yields only once the call
// has returned, so a spec's transaction has committed by then; a sibling
// branch of a ParallelAgent does not carry it out early. A call made by a
// sub-agent that agenttool runs counts as part of the delegating call, whose
// result is the one the client sees. ADK runs a streaming tool without the
// tool callbacks, so its call has no id here: what it publishes goes out after
// the run's next event, its result unless a ParallelAgent sibling yields first.
// One published outside a call, from a model callback say, goes out ahead of
// the run's next event. A call that publishes and then fails still sends it,
// which costs the page a refetch of unchanged data.
//
// It reports whether a run was listening. Outside a bridge run, or with no
// keys, nothing is queued and it returns false.
func PublishInvalidation(ctx context.Context, reason string, keys ...string) bool {
	s := sinkFrom(ctx)
	if s == nil || len(keys) == 0 {
		return false
	}
	value := invalidation{Keys: slices.Clone(keys)}
	if reason != "" {
		value.Reason = &reason
	}
	s.publish(ctx, aguix.Custom(InvalidateEventName, value))
	return true
}
