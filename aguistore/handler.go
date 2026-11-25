package aguistore

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/aguix"
)

// HandlerOption configures Handler.
type HandlerOption func(*handler)

// WithListLimit caps how many threads a list returns, 100 by default; a
// request's ?limit can ask for fewer.
func WithListLimit(n int) HandlerOption {
	return func(h *handler) { h.limit = n }
}

// Handler serves a user's threads as django-ag-ui's ThreadsView does, the
// routes the web component's data-threads-url reads, relative to where it is
// mounted (strip the prefix with http.StripPrefix):
//
//   - GET  / lists the threads, {"threads": [{thread_id, title, updated_at,
//     preview, parent}]}, with an ETag that If-None-Match answers with 304;
//   - GET  / with Accept: text/event-stream is the threads watch: an SSE frame,
//     id: the list's ETag and data: the list, each time the list changes
//     (none for the ETag a Last-Event-ID names), comment heartbeats between,
//     until WithWatch's duration ends it for the client to reconnect;
//   - GET  /<id>/ returns {"thread_id", "messages"};
//   - PATCH /<id>/ takes {"title"} and answers {"thread_id", "title"};
//   - DELETE /<id>/ answers 204.
//
// A store that is a RunStore adds each row's status, last_read_at, reason
// when failed and interrupts when waiting, as RUN_FINISHED sent them, which
// is what tells the web component a run store is there; and
// POST /<id>/read/ marks the thread read, answering 204.
//
// userID resolves the caller as aguiadk.WithUserID does, and should be the
// same function: an error or an empty id is refused with 401, or 403 for an
// error wrapping services.ErrPermission. Another user's thread is 404.
func Handler(store Store, userID func(context.Context) (string, error), opts ...HandlerOption) http.Handler {
	h := &handler{
		store: store, userID: userID, limit: 100,
		poll: 2 * time.Second, heartbeat: 15 * time.Second, watchFor: 5 * time.Minute,
	}
	h.runs, _ = store.(RunStore)
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// WithWatch sets how often the threads watch reads the list, every 2s by
// default; how long it stays quiet before a heartbeat comment, 15s (zero sends
// none); and how long one watch lasts before it ends, 5 minutes, so a client
// reconnects and is authenticated again. django-ag-ui's are the same.
func WithWatch(poll, heartbeat, duration time.Duration) HandlerOption {
	return func(h *handler) { h.poll, h.heartbeat, h.watchFor = poll, heartbeat, duration }
}

type handler struct {
	store  Store
	runs   RunStore
	userID func(context.Context) (string, error)
	limit  int

	poll, heartbeat, watchFor time.Duration
}

// threadRow's run keys are raw so a served status can say null, as
// django-ag-ui's does, and the interrupts keep the bytes they were stored as.
type threadRow struct {
	ThreadID   string          `json:"thread_id"`
	Title      string          `json:"title"`
	UpdatedAt  string          `json:"updated_at"`
	Preview    string          `json:"preview"`
	Parent     any             `json:"parent"`
	Status     string          `json:"status,omitempty"`
	LastReadAt json.RawMessage `json:"last_read_at,omitempty"`
	Reason     json.RawMessage `json:"reason,omitempty"`
	Interrupts json.RawMessage `json:"interrupts,omitempty"`
}

const maxTitle = 255

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, err := h.userID(r.Context())
	if err != nil || user == "" {
		status := http.StatusUnauthorized
		if errors.Is(err, services.ErrPermission) {
			status = http.StatusForbidden
		}
		writeError(w, status, http.StatusText(status))
		return
	}
	id := strings.Trim(r.URL.Path, "/")
	switch {
	case id == "":
		h.list(w, r, user)
	case h.runs != nil && strings.Count(id, "/") == 1 && strings.HasSuffix(id, "/read"):
		h.read(w, r, user, strings.TrimSuffix(id, "/read"))
	case strings.Contains(id, "/"):
		writeError(w, http.StatusNotFound, "not found")
	default:
		h.detail(w, r, user, id)
	}
}

func (h *handler) list(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodGet {
		notAllowed(w, "GET")
		return
	}
	limit := h.limit
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n < limit {
		limit = n
	}
	body, etag, err := h.listing(r.Context(), user, limit)
	if err != nil {
		storeError(w, err)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		if f, ok := w.(http.Flusher); ok {
			h.watch(w, f, r, user, limit, body, etag)
			return
		}
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Add("Vary", "Accept")
	if matches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// listing is the list body and its ETag.
func (h *handler) listing(ctx context.Context, user string, limit int) ([]byte, string, error) {
	threads, err := h.store.ListThreads(ctx, user, limit)
	if err != nil {
		return nil, "", err
	}
	rows := make([]threadRow, 0, len(threads))
	for _, t := range threads {
		row := threadRow{
			ThreadID: t.ID, Title: t.Title, Preview: t.Preview,
			UpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339Nano),
		}
		if h.runs != nil {
			h.status(&row, t)
		}
		rows = append(rows, row)
	}
	body, err := json.Marshal(map[string]any{"threads": rows})
	if err != nil {
		return nil, "", fmt.Errorf("aguistore: a stored thread's interrupts do not encode: %w", err)
	}
	sum := sha256.Sum256(body)
	return body, `"` + hex.EncodeToString(sum[:16]) + `"`, nil
}

// status fills a row's run keys as django-ag-ui's thread_status_fields does.
func (h *handler) status(row *threadRow, t Thread) {
	row.Status, row.LastReadAt = cmp.Or(t.Status, StatusIdle), json.RawMessage("null")
	if !t.LastReadAt.IsZero() {
		row.LastReadAt, _ = json.Marshal(t.LastReadAt.UTC().Format(time.RFC3339Nano))
	}
	switch row.Status {
	case StatusFailed:
		row.Reason = json.RawMessage("null")
		if t.Reason != "" {
			row.Reason, _ = json.Marshal(t.Reason)
		}
	case StatusWaiting:
		row.Interrupts = t.Interrupts
		if len(row.Interrupts) == 0 {
			row.Interrupts = json.RawMessage("[]")
		}
	}
}

func (h *handler) read(w http.ResponseWriter, r *http.Request, user, id string) {
	if r.Method != http.MethodPost {
		notAllowed(w, "POST")
		return
	}
	if err := h.runs.MarkRead(r.Context(), user, id); err != nil {
		storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// matches reports whether an If-None-Match value names etag: in a list, as *,
// or weakened by a proxy's compression.
func matches(header, etag string) bool {
	for tag := range strings.SplitSeq(header, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || strings.TrimPrefix(tag, "W/") == etag {
			return true
		}
	}
	return false
}

func (h *handler) detail(w http.ResponseWriter, r *http.Request, user, id string) {
	switch r.Method {
	case http.MethodGet:
		messages, ok, err := h.store.LoadThread(r.Context(), user, id)
		if err != nil {
			storeError(w, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"thread_id": id, "messages": nonNil(messages)})
	case http.MethodPatch:
		var body struct {
			Title any `json:"title"`
		}
		err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body)
		title, _ := body.Title.(string)
		if title = strings.TrimSpace(title); err != nil || title == "" {
			writeError(w, http.StatusBadRequest, "a non-empty 'title' is required")
			return
		}
		if utf8.RuneCountInString(title) > maxTitle {
			title = string([]rune(title)[:maxTitle])
		}
		if err := h.store.RenameThread(r.Context(), user, id, title); err != nil {
			storeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"thread_id": id, "title": title})
	case http.MethodDelete:
		if err := h.store.DeleteThread(r.Context(), user, id); err != nil {
			storeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		notAllowed(w, "GET, PATCH, DELETE")
	}
}

func nonNil(messages []aguix.Message) []aguix.Message {
	if messages == nil {
		return []aguix.Message{}
	}
	return messages
}

// storeError answers a store's not found as 404 and anything else as 500,
// without its text, which may name the database.
func storeError(w http.ResponseWriter, err error) {
	if errors.Is(err, services.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal error")
}

func notAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// writeJSON answers body, or 500 when a store's messages do not encode.
func writeJSON(w http.ResponseWriter, status int, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		status, encoded = http.StatusInternalServerError, []byte(`{"error":"internal error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}
