package aguistore

import (
	"fmt"
	"net/http"
	"time"
)

// watch streams the list as django-ag-ui's threads watch does: a frame for each
// list the client has not seen, read every poll, until the request ends or the
// watch's duration passes. A store error ends it quietly, as a dropped
// connection would, for the client to reconnect.
func (h *handler) watch(w http.ResponseWriter, f http.Flusher, r *http.Request, user string, limit int, body []byte, etag string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Add("Vary", "Accept")
	w.WriteHeader(http.StatusOK)
	f.Flush()

	seen := r.Header.Get("Last-Event-ID")
	end := time.NewTimer(h.watchFor)
	defer end.Stop()
	poll := time.NewTicker(h.poll)
	defer poll.Stop()
	quiet := time.NewTimer(h.heartbeat)
	defer quiet.Stop()
	hush := func() {
		if h.heartbeat > 0 {
			quiet.Reset(h.heartbeat)
		} else {
			quiet.Stop()
		}
	}
	hush()
	for {
		if etag != seen {
			if _, err := fmt.Fprintf(w, "id: %s\ndata: %s\n\n", etag, body); err != nil {
				return
			}
			f.Flush()
			seen = etag
			hush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-end.C:
			return
		case <-quiet.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			f.Flush()
			hush()
		case <-poll.C:
			var err error
			if body, etag, err = h.listing(r.Context(), user, limit); err != nil {
				return
			}
		}
	}
}
