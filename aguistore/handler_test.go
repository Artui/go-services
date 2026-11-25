package aguistore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/aguix"
)

type ctxUser struct{}

// byHeader names the caller from X-User, as a server's authentication would
// put it on the request's context.
func byHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxUser{}, r.Header.Get("X-User"))))
	})
}

func userFromContext(ctx context.Context) (string, error) {
	user, _ := ctx.Value(ctxUser{}).(string)
	if user == "denied" {
		return "", fmt.Errorf("%w: suspended", services.ErrPermission)
	}
	if user == "broken" {
		return user, errors.New("no session")
	}
	return user, nil
}

// mount serves the store's routes under /threads, as a server would.
func mount(s Store, opts ...HandlerOption) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/threads/", http.StripPrefix("/threads", Handler(s, userFromContext, opts...)))
	return byHeader(mux)
}

type response struct {
	status int
	header http.Header
	body   string
}

func do(t *testing.T, h http.Handler, method, path, user, body string, headers ...string) response {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-User", user)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	got, _ := io.ReadAll(w.Result().Body)
	return response{w.Code, w.Header(), string(got)}
}

func seeded(t *testing.T) *Memory {
	t.Helper()
	s := NewMemory(WithClock(newClock().now))
	mustSave(t, s, "alice", "th1", msg("u1", "user", "First."), msg("a1", "assistant", "Answer."))
	mustSave(t, s, "alice", "th2", msg("u2", "user", "Second."))
	mustSave(t, s, "bob", "th9", msg("u9", "user", "Bob's."))
	return s
}

func TestTheListIsTheUsersThreadsAsTheWebComponentReadsThem(t *testing.T) {
	h := mount(seeded(t))
	got := do(t, h, "GET", "/threads/", "alice", "")
	want := `{"threads":[` +
		`{"thread_id":"th2","title":"Second.","updated_at":"2026-11-25T09:00:02Z","preview":"Second.","parent":null},` +
		`{"thread_id":"th1","title":"First.","updated_at":"2026-11-25T09:00:01Z","preview":"Answer.","parent":null}]}`
	if got.status != 200 || got.body != want {
		t.Fatalf("%d %s\nwant %s", got.status, got.body, want)
	}
	if got.header.Get("Cache-Control") != "private, no-cache" || got.header.Get("Vary") != "Accept" ||
		got.header.Get("Content-Type") != "application/json" {
		t.Errorf("headers %v", got.header)
	}
	if limited := do(t, h, "GET", "/threads/?limit=1", "alice", ""); !strings.Contains(limited.body, "th2") ||
		strings.Contains(limited.body, "th1") {
		t.Errorf("limit=1 listed %s", limited.body)
	}
	for _, q := range []string{"0", "-1", "x"} {
		if all := do(t, h, "GET", "/threads/?limit="+q, "alice", ""); !strings.Contains(all.body, "th1") {
			t.Errorf("limit=%s listed %s", q, all.body)
		}
	}
	if capped := do(t, mount(seeded(t), WithListLimit(1)), "GET", "/threads/?limit=5", "alice", ""); strings.Contains(capped.body, "th1") {
		t.Errorf("?limit passed the cap: %s", capped.body)
	}
	if bob := do(t, h, "GET", "/threads/", "bob", ""); !strings.Contains(bob.body, "th9") || strings.Contains(bob.body, "th1") {
		t.Errorf("bob lists %s", bob.body)
	}
	if empty := do(t, mount(NewMemory()), "GET", "/threads/", "carol", ""); empty.body != `{"threads":[]}` {
		t.Errorf("no threads: %s", empty.body)
	}
}

func TestTheListAnswersIfNoneMatchWith304(t *testing.T) {
	s := seeded(t)
	h := mount(s)
	etag := do(t, h, "GET", "/threads/", "alice", "").header.Get("ETag")
	if len(etag) != 34 || etag[0] != '"' {
		t.Fatalf("etag %q", etag)
	}
	for _, header := range []string{etag, `"other", ` + etag, "W/" + etag, "*"} {
		got := do(t, h, "GET", "/threads/", "alice", "", "If-None-Match", header)
		if got.status != http.StatusNotModified || got.body != "" || got.header.Get("ETag") != etag {
			t.Errorf("If-None-Match %s: %d %q", header, got.status, got.body)
		}
	}
	if got := do(t, h, "GET", "/threads/", "alice", "", "If-None-Match", `"other"`); got.status != 200 {
		t.Errorf("a stale tag answered %d", got.status)
	}
	mustSave(t, s, "alice", "th1", msg("u1", "user", "First."))
	if got := do(t, h, "GET", "/threads/", "alice", "", "If-None-Match", etag); got.status != 200 ||
		got.header.Get("ETag") == etag {
		t.Errorf("after a save: %d %s", got.status, got.header.Get("ETag"))
	}
}

func TestAThreadIsReadRenamedAndDeleted(t *testing.T) {
	s := seeded(t)
	h := mount(s)
	got := do(t, h, "GET", "/threads/th1/", "alice", "")
	want := `{"messages":[{"id":"u1","role":"user","content":"First."},` +
		`{"id":"a1","role":"assistant","content":"Answer."}],"thread_id":"th1"}`
	if got.status != 200 || got.body != want {
		t.Errorf("read: %d %s", got.status, got.body)
	}
	if got := do(t, h, "PATCH", "/threads/th1/", "alice", `{"title":"  Renamed  "}`); got.status != 200 ||
		got.body != `{"thread_id":"th1","title":"Renamed"}` {
		t.Errorf("rename: %d %s", got.status, got.body)
	}
	long := strings.Repeat("é", 300)
	if got := do(t, h, "PATCH", "/threads/th1", "alice", `{"title":"`+long+`"}`); got.status != 200 ||
		!strings.Contains(got.body, `"`+strings.Repeat("é", 255)+`"`) {
		t.Errorf("a long title: %d %s", got.status, got.body)
	}
	if got := do(t, h, "DELETE", "/threads/th1/", "alice", ""); got.status != http.StatusNoContent || got.body != "" {
		t.Errorf("delete: %d %s", got.status, got.body)
	}
	if got := do(t, h, "GET", "/threads/th1/", "alice", ""); got.status != 404 {
		t.Errorf("read after delete: %d", got.status)
	}
	empty := NewMemory()
	mustSave(t, empty, "alice", "none")
	if got := do(t, mount(empty), "GET", "/threads/none/", "alice", ""); got.body != `{"messages":[],"thread_id":"none"}` {
		t.Errorf("an empty thread: %s", got.body)
	}
}

func TestAnotherUsersThreadIsNotFound(t *testing.T) {
	s := seeded(t)
	h := mount(s)
	for _, tc := range []struct{ method, body string }{{"GET", ""}, {"PATCH", `{"title":"Mine"}`}} {
		if got := do(t, h, tc.method, "/threads/th1/", "bob", tc.body); got.status != 404 || got.body != `{"error":"not found"}` {
			t.Errorf("bob's %s: %d %s", tc.method, got.status, got.body)
		}
	}
	do(t, h, "DELETE", "/threads/th1/", "bob", "")
	if got := do(t, h, "GET", "/threads/th1/", "alice", ""); got.status != 200 || !strings.Contains(got.body, "First.") {
		t.Errorf("alice's thread after bob's delete: %d %s", got.status, got.body)
	}
}

func TestBadRequestsAreRefused(t *testing.T) {
	h := mount(seeded(t))
	for _, tc := range []struct {
		method, path, user, body string
		status                   int
		allow                    string
	}{
		{"PATCH", "/threads/th1/", "alice", `{"title":"  "}`, 400, ""},
		{"PATCH", "/threads/th1/", "alice", `{"title":7}`, 400, ""},
		{"PATCH", "/threads/th1/", "alice", `not json`, 400, ""},
		{"PATCH", "/threads/nope/", "alice", `{"title":"x"}`, 404, ""},
		{"PATCH", "/threads/th1/", "alice", `{"title":"` + strings.Repeat("a", 70<<10) + `"}`, 400, ""},
		{"POST", "/threads/", "alice", "", 405, "GET"},
		{"PUT", "/threads/th1/", "alice", "", 405, "GET, PATCH, DELETE"},
		{"GET", "/threads/th1/children/", "alice", "", 404, ""},
		{"DELETE", "/threads/th1/children/", "alice", "", 404, ""},
		{"GET", "/threads/", "", "", 401, ""},
		{"GET", "/threads/", "broken", "", 401, ""},
		{"GET", "/threads/th1/", "denied", "", 403, ""},
	} {
		got := do(t, h, tc.method, tc.path, tc.user, tc.body)
		if got.status != tc.status || got.header.Get("Allow") != tc.allow || !strings.HasPrefix(got.body, `{"error":`) {
			t.Errorf("%s %s as %q: %d allow %q %s", tc.method, tc.path, tc.user, got.status, got.header.Get("Allow"), got.body)
		}
	}
}

// failing is a store whose every call fails, with an error naming the database.
type failing struct{ *Memory }

var errDB = errors.New("dial tcp db.internal:5432: refused")

func (failing) LoadThread(context.Context, string, string) ([]aguix.Message, bool, error) {
	return nil, false, errDB
}
func (failing) ListThreads(context.Context, string, int) ([]Thread, error) { return nil, errDB }
func (failing) RenameThread(context.Context, string, string, string) error { return errDB }
func (failing) DeleteThread(context.Context, string, string) error         { return errDB }

// unencodable answers a thread whose content is not JSON.
type unencodable struct{ *Memory }

func (unencodable) LoadThread(context.Context, string, string) ([]aguix.Message, bool, error) {
	return []aguix.Message{{ID: "u1", Role: "user", Content: json.RawMessage("{")}}, true, nil
}

func TestAStoreFailureIsA500ThatDoesNotNameTheDatabase(t *testing.T) {
	h := mount(failing{NewMemory()})
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/threads/", ""}, {"GET", "/threads/th1/", ""},
		{"PATCH", "/threads/th1/", `{"title":"x"}`}, {"DELETE", "/threads/th1/", ""},
	} {
		if got := do(t, h, tc.method, tc.path, "alice", tc.body); got.status != 500 || got.body != `{"error":"internal error"}` {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, got.status, got.body)
		}
	}
	if got := do(t, mount(unencodable{NewMemory()}), "GET", "/threads/th1/", "alice", ""); got.status != 500 ||
		got.body != `{"error":"internal error"}` {
		t.Errorf("an unencodable thread: %d %s", got.status, got.body)
	}
}
