package aguix

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
