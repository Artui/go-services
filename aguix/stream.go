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
	if _, err := fmt.Fprintf(s.writer, "data: %s\n\n", payload); err != nil {
		return err
	}
	s.flusher.Flush()
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

func isObjectOrAbsent(value any) bool {
	if value == nil {
		return true
	}
	raw, err := json.Marshal(value)
	return err == nil && len(raw) > 0 && raw[0] == '{'
}
