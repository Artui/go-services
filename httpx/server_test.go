package httpx_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Artui/go-services/httpx"
)

// A recorder does not behave like a server: it keeps a body under a status that
// forbids one, and it never negotiates a connection. What follows goes over a
// real one, because those are the two things this adapter gets wrong silently.
func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := mustMount(t, map[string]httpx.Route{
		"get_author":    {Method: http.MethodGet, Pattern: "/authors/{id}"},
		"create_author": {Method: http.MethodPost, Pattern: "/authors"},
		"delete_author": {Method: http.MethodDelete, Pattern: "/authors/{id}"},
	}, func(r *http.Request) (any, error) { return r.Header.Get("X-Viewer"), nil })

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// call runs one request and returns the status and body. It reports failures by
// returning them rather than through testing.T, so that it is also safe to call
// from the goroutines the concurrency test starts.
func call(ctx context.Context, srv *httptest.Server, method, url string) (*http.Response, string, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return resp, string(body), nil
}

func TestOverARealServer(t *testing.T) {
	srv := newServer(t)

	t.Run("a query answers with its value", func(t *testing.T) {
		resp, body, err := call(t.Context(), srv, http.MethodGet, srv.URL+"/authors/4")
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, body)
		}
		if got := resp.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if !strings.Contains(body, `"id":4`) {
			t.Errorf("body = %s", body)
		}
	})

	t.Run("HEAD reaches a GET route with no body", func(t *testing.T) {
		// ServeMux routes HEAD to a GET pattern. It matters that this is known
		// rather than assumed: it is why a mutation is refused on HEAD as well
		// as on GET, since refusing only GET would leave the operation
		// reachable by the same prefetch under the other name.
		resp, body, err := call(t.Context(), srv, http.MethodHead, srv.URL+"/authors/4")
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if body != "" {
			t.Errorf("body = %q, want none on a HEAD", body)
		}
		// A HEAD still describes the GET it stands for, so the header stays.
		if got := resp.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want the one the GET would carry", got)
		}
	})

	t.Run("a 204 really carries nothing", func(t *testing.T) {
		resp, body, err := call(t.Context(), srv, http.MethodDelete, srv.URL+"/authors/4")
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", resp.StatusCode)
		}
		if body != "" {
			t.Errorf("body = %q, want none", body)
		}
		// Unlike a HEAD, a 204 is not standing in for anything, so a
		// Content-Type on it describes content that does not exist.
		if got := resp.Header.Get("Content-Type"); got != "" {
			t.Errorf("Content-Type = %q, want none", got)
		}
	})
}

// One handler serves every request for its route. Its doc comment says it holds
// no state of its own; under the race detector, this is what turns that from an
// assertion into a claim somebody checked.
func TestOneHandlerServesConcurrentRequests(t *testing.T) {
	srv := newServer(t)

	const callers = 16
	var wg sync.WaitGroup
	failures := make(chan string, callers)

	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := strings.Repeat("1", 1+i%3)
			resp, body, err := call(t.Context(), srv, http.MethodGet, srv.URL+"/authors/"+id)
			switch {
			case err != nil:
				failures <- err.Error()
			case resp.StatusCode != http.StatusOK:
				failures <- fmt.Sprintf("status %d: %s", resp.StatusCode, body)
			case !strings.Contains(body, `"id":`+id):
				// Two callers must not see each other's captures.
				failures <- fmt.Sprintf("asked for %s, got %s", id, body)
			}
		}()
	}
	wg.Wait()
	close(failures)

	for failure := range failures {
		t.Errorf("a concurrent request failed: %s", failure)
	}
}
