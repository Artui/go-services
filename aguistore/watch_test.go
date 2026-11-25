package aguistore

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// serve starts h, closed after the watches opened on it have been cancelled.
func serve(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// watching opens the threads watch on srv as user, with extra headers.
func watching(t *testing.T, srv *httptest.Server, user string, headers ...string) (*http.Response, *bufio.Reader) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	r, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/threads/", nil)
	r.Header.Set("Accept", "text/event-stream")
	r.Header.Set("X-User", user)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	resp, err := srv.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp, bufio.NewReader(resp.Body)
}

// frame reads one SSE frame, its lines joined.
func frame(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("the stream ended after %q: %v", lines, err)
		}
		if line == "\n" {
			return strings.Join(lines, "")
		}
		lines = append(lines, line)
	}
}

func TestTheWatchSendsAFrameWhenAStatusChanges(t *testing.T) {
	s := seeded(t)
	srv := serve(t, mount(s, WithWatch(5*time.Millisecond, time.Hour, time.Minute)))
	resp, body := watching(t, srv, "alice")
	if resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-Accel-Buffering") != "no" ||
		resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("Vary") != "Accept" {
		t.Errorf("headers %v", resp.Header)
	}
	first := frame(t, body)
	poll := do(t, mount(s), "GET", "/threads/", "alice", "")
	if want := "id: " + poll.header.Get("ETag") + "\ndata: " + poll.body + "\n"; first != want {
		t.Fatalf("the first frame is not the list:\n%s\nwant\n%s", first, want)
	}
	if _, err := s.StartRun(context.Background(), "alice", "th1", "r1", nil); err != nil {
		t.Fatal(err)
	}
	next := frame(t, body)
	if !strings.Contains(next, `"thread_id":"th1","title":"First.","updated_at":"2026-11-25T09:00:01Z","preview":"Answer.","parent":null,"status":"running"`) ||
		strings.HasPrefix(next, "id: "+poll.header.Get("ETag")) {
		t.Errorf("after the run started: %s", next)
	}
}

func TestTheWatchSkipsTheListTheClientHasAndBeatsWhileQuiet(t *testing.T) {
	s := seeded(t)
	etag := do(t, mount(s), "GET", "/threads/", "alice", "").header.Get("ETag")
	srv := serve(t, mount(s, WithWatch(time.Millisecond, 20*time.Millisecond, time.Minute)))
	_, body := watching(t, srv, "alice", "Last-Event-ID", etag)
	for range 2 {
		if got := frame(t, body); got != ": heartbeat\n" {
			t.Fatalf("a quiet watch sent %q", got)
		}
	}
}

func TestTheWatchEndsAfterItsDuration(t *testing.T) {
	srv := serve(t, mount(seeded(t), WithWatch(time.Millisecond, 0, 30*time.Millisecond)))
	_, body := watching(t, srv, "alice")
	rest, err := io.ReadAll(body)
	if err != nil || strings.Count(string(rest), "id: ") != 1 || strings.Contains(string(rest), "heartbeat") {
		t.Errorf("%q %v", rest, err)
	}
}

// flaky lists once, then fails.
type flaky struct {
	*Memory
	calls atomic.Int32
}

func (f *flaky) ListThreads(ctx context.Context, user string, limit int) ([]Thread, error) {
	if f.calls.Add(1) > 1 {
		return nil, errors.New("gone")
	}
	return f.Memory.ListThreads(ctx, user, limit)
}

func TestAStoreErrorEndsTheWatch(t *testing.T) {
	srv := serve(t, mount(&flaky{Memory: seeded(t)}, WithWatch(time.Millisecond, 0, time.Minute)))
	_, body := watching(t, srv, "alice")
	if rest, err := io.ReadAll(body); err != nil || strings.Count(string(rest), "id: ") != 1 {
		t.Errorf("%q %v", rest, err)
	}
	failing := mount(listed{RunStore: NewMemory(), err: errors.New("down")})
	if got := do(t, failing, "GET", "/threads/", "alice", "", "Accept", "text/event-stream"); got.status != 500 {
		t.Errorf("a watch over a failing store: %d", got.status)
	}
}

// unflushed hides the recorder's Flush, as a writer that cannot stream would.
type unflushed struct{ http.ResponseWriter }

func TestAWriterThatCannotStreamIsAnsweredAsAPoll(t *testing.T) {
	h := mount(seeded(t))
	r := httptest.NewRequest("GET", "/threads/", nil)
	r.Header.Set("Accept", "text/event-stream")
	r.Header.Set("X-User", "alice")
	w := httptest.NewRecorder()
	h.ServeHTTP(unflushed{w}, r)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("ETag") == "" {
		t.Errorf("%d %v", w.Code, w.Header())
	}
}

func TestAWatchStopsWhenTheClientLeaves(t *testing.T) {
	h := Handler(seeded(t), func(context.Context) (string, error) { return "alice", nil },
		WithWatch(time.Hour, time.Hour, time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequestWithContext(ctx, "GET", "/", nil)
	r.Header.Set("Accept", "text/event-stream")
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), r)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch outlived its client")
	}
}

// hungUp is a streaming writer whose client has gone: every write fails.
type hungUp struct{ *httptest.ResponseRecorder }

func (hungUp) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestAWatchStopsWhenAWriteFails(t *testing.T) {
	s := seeded(t)
	etag := do(t, mount(s), "GET", "/threads/", "alice", "").header.Get("ETag")
	h := Handler(s, func(context.Context) (string, error) { return "alice", nil },
		WithWatch(time.Hour, time.Millisecond, time.Hour))
	for _, seen := range []string{"", etag} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept", "text/event-stream")
		r.Header.Set("Last-Event-ID", seen)
		done := make(chan struct{})
		go func() {
			h.ServeHTTP(hungUp{httptest.NewRecorder()}, r)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("a watch seen at %q outlived a failed write", seen)
		}
	}
}
