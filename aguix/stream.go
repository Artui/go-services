package aguix

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// Stream writes AG-UI events as Server-Sent Events.
//
// The framing is one "data:" line carrying the JSON event, then a blank line.
// No "event:" line: the client reads each SSE message's data and dispatches on
// the JSON's own type field, so a name in the SSE envelope would be a second
// discriminator that nothing reads and that could disagree with the first.
type Stream struct {
	writer  io.Writer
	flusher http.Flusher

	mu      sync.Mutex
	outcome *RunOutcome
	usage   []TokenUsage
	open    []Event

	observe func(Event)
}

// NewStream prepares w for streaming and writes the response headers.
//
// The headers go out before any event, which is what makes the connection a
// stream rather than a slow response: a client that has not seen
// Content-Type: text/event-stream has no reason to start parsing.
//
// X-Accel-Buffering is for nginx, which otherwise buffers a proxied response
// and delivers the whole run at the end -- the events all arrive, in order,
// after the answer is no longer interesting. It is meaningless elsewhere and
// harmless there.
func NewStream(w http.ResponseWriter) (*Stream, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Not a client error and not something a retry fixes: the server was
		// built on a ResponseWriter that cannot stream.
		return nil, fmt.Errorf("aguix: the response writer cannot flush, so it cannot stream")
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	return &Stream{writer: w, flusher: flusher}, nil
}

// Emit writes one event and flushes it.
//
// Flushing per event is the point of the transport. A buffered stream delivers
// a correct sequence at the wrong time, and the difference between an agent
// that types and an agent that pauses then dumps is entirely here.
func (s *Stream) Emit(event Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.writer, "data: %s\n\n", payload); err != nil {
		return err
	}
	s.flusher.Flush()
	s.track(event)
	if s.observe != nil {
		s.observe(event)
	}
	return nil
}

// spanEnds pairs each span-opening event with the event that closes it.
var spanEnds = map[EventType]EventType{
	EventTextMessageStart:      EventTextMessageEnd,
	EventToolCallStart:         EventToolCallEnd,
	EventReasoningStart:        EventReasoningEnd,
	EventReasoningMessageStart: EventReasoningMessageEnd,
	EventSubagentStarted:       EventSubagentError,
}

// unfinishedSubagent is the error a cut-off run closes an open sub-agent with,
// in django-ag-ui's words and code: it did not fail, it was never let finish.
const unfinishedSubagent = "the sub-agent was still running when the run ended"

// closes reports whether event ends the span that open began. A sub-agent ends
// on either SUBAGENT_FINISHED or SUBAGENT_ERROR.
func closes(open, event Event) bool {
	end := spanEnds[open.Type]
	if open.Type == EventSubagentStarted && event.Type == EventSubagentFinished {
		end = event.Type
	}
	return end == event.Type && spanID(open) == spanID(event)
}

// track keeps the spans still open, which the client's verifier refuses to see
// a RUN_FINISHED over. Only a run the handler cut off has any left to close.
func (s *Stream) track(event Event) {
	if _, opens := spanEnds[event.Type]; opens {
		s.open = append(s.open, event)
		return
	}
	for i := len(s.open) - 1; i >= 0; i-- {
		if closes(s.open[i], event) {
			s.open = append(s.open[:i], s.open[i+1:]...)
			return
		}
	}
}

func spanID(event Event) string {
	switch {
	case event.Type == EventSubagentStarted || event.Type == EventSubagentFinished ||
		event.Type == EventSubagentError:
		return event.SubagentRunID
	case event.ToolCallID != "":
		return event.ToolCallID
	}
	return event.MessageID
}

// closeOpen ends every span still open, innermost first.
func (s *Stream) closeOpen() error {
	s.mu.Lock()
	open := append([]Event(nil), s.open...)
	s.mu.Unlock()
	for i := len(open) - 1; i >= 0; i-- {
		end := Event{Type: spanEnds[open[i].Type], SubagentRunID: open[i].SubagentRunID}
		switch open[i].Type {
		case EventToolCallStart:
			end.ToolCallID = open[i].ToolCallID
		case EventSubagentStarted:
			end = SubagentError(open[i].SubagentRunID, unfinishedSubagent).WithCode("unfinished")
		default:
			end.MessageID = open[i].MessageID
		}
		if err := s.Emit(end); err != nil {
			return err
		}
	}
	return nil
}

// Finish sets how the run ends, for the handler to write as RUN_FINISHED's
// outcome. Without it the run finishes bare, as it always did.
//
// The handler still ends the run in RUN_ERROR if the agent then returns an
// error, and a later Finish replaces an earlier one. Finish checks what makes
// the client's parser reject the frame and end its run: an interrupt needs at
// least one entry, and its responseSchema and metadata must be JSON objects.
// It also holds this package's own rule that ids and reasons are not empty.
func (s *Stream) Finish(outcome RunOutcome) error {
	switch outcome.Type {
	case "success":
		for _, id := range outcome.PendingToolCallIDs {
			if id == "" {
				return errors.New("aguix: a pending tool call id is empty")
			}
		}
		outcome.Interrupts = nil
	case "interrupt":
		if len(outcome.Interrupts) == 0 {
			return errors.New("aguix: an interrupt outcome needs at least one interrupt")
		}
		for _, interrupt := range outcome.Interrupts {
			if interrupt.ID == "" || interrupt.Reason == "" {
				return errors.New("aguix: an interrupt needs an id and a reason")
			}
			if !isObjectOrAbsent(interrupt.ResponseSchema) || !isObjectOrAbsent(interrupt.Metadata) {
				return errors.New("aguix: an interrupt's responseSchema and metadata must be JSON objects")
			}
		}
		outcome.PendingToolCallIDs = nil
	case "cancelled":
		outcome.PendingToolCallIDs, outcome.Interrupts = nil, nil
	default:
		return fmt.Errorf("aguix: unknown outcome type %q", outcome.Type)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcome = &outcome
	return nil
}

func (s *Stream) finishedWith() (RunOutcome, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outcome == nil {
		return RunOutcome{}, false
	}
	return *s.outcome, true
}

// maxTokenCount is JavaScript's Number.MAX_SAFE_INTEGER, the ceiling the
// client's schema puts on every count.
const maxTokenCount = 1<<53 - 1

// SetUsage reports the run's token usage, for the handler to write on
// RUN_FINISHED. It is never written on RUN_ERROR, and a later call replaces an
// earlier one, so an agent that keeps a running sum sets the whole sum each time.
//
// An entry with neither input nor output tokens says nothing and is dropped, as
// pydantic-ai drops it; usage left with no entries is absent from the frame. A
// count below zero or above 2^53-1 is refused, since the client's parser rejects
// the frame and ends the run on its side alone.
func (s *Stream) SetUsage(usage ...TokenUsage) error {
	var kept []TokenUsage
	for _, entry := range usage {
		for _, count := range []int64{
			entry.InputTokens, entry.OutputTokens, entry.TotalTokens,
			entry.ReasoningTokens, entry.CachedInputTokens, entry.CacheWriteInputTokens,
		} {
			if count < 0 || count > maxTokenCount {
				return fmt.Errorf("aguix: a token count of %d is outside 0 to 2^53-1", count)
			}
		}
		if entry.InputTokens != 0 || entry.OutputTokens != 0 {
			kept = append(kept, entry)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage = kept
	return nil
}

func (s *Stream) usedTokens() []TokenUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage
}

func isObjectOrAbsent(value any) bool {
	if value == nil {
		return true
	}
	raw, err := json.Marshal(value)
	return err == nil && len(raw) > 0 && raw[0] == '{'
}
