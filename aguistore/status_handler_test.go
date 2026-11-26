package aguistore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// listed is a run store that lists the threads it is given.
type listed struct {
	RunStore
	threads []Thread
	err     error
}

func (l listed) ListThreads(context.Context, string, int) ([]Thread, error) { return l.threads, l.err }

func TestTheListCarriesEachThreadsRunStatus(t *testing.T) {
	s := seeded(t)
	ctx := context.Background()
	if _, _, err := s.StartRun(ctx, "alice", "th1", "r1", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, "alice", "th1", "r1", StatusWaiting, "", json.RawMessage(parked)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartRun(ctx, "alice", "th2", "r2", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, "alice", "th2", "r2", StatusFailed, "error", nil); err != nil {
		t.Fatal(err)
	}
	h := mount(s)
	if got := do(t, h, "POST", "/threads/th2/read/", "alice", ""); got.status != http.StatusNoContent || got.body != "" {
		t.Fatalf("read: %d %s", got.status, got.body)
	}
	got := do(t, h, "GET", "/threads/", "alice", "")
	want := `{"threads":[` +
		`{"thread_id":"th2","title":"Second.","updated_at":"2026-11-25T09:00:02Z","preview":"Second.","parent":null,` +
		`"status":"failed","last_read_at":"2026-11-25T09:00:04Z","reason":"error"},` +
		`{"thread_id":"th1","title":"First.","updated_at":"2026-11-25T09:00:01Z","preview":"Answer.","parent":null,` +
		`"status":"waiting","last_read_at":null,"interrupts":` + parked + `}]}`
	if got.status != 200 || got.body != want {
		t.Fatalf("%d %s\nwant %s", got.status, got.body, want)
	}
	if bob := do(t, h, "GET", "/threads/", "bob", ""); !strings.Contains(bob.body, `"status":"idle","last_read_at":null}`) {
		t.Errorf("a thread with no run: %s", bob.body)
	}
}

func TestAFailedRowWithNoReasonAndAWaitingRowWithNoInterruptsStillParse(t *testing.T) {
	h := mount(listed{RunStore: NewMemory(), threads: []Thread{
		{ID: "a", Status: StatusFailed}, {ID: "b", Status: StatusWaiting},
	}})
	got := do(t, h, "GET", "/threads/", "alice", "")
	if !strings.Contains(got.body, `"status":"failed","last_read_at":null,"reason":null}`) ||
		!strings.Contains(got.body, `"status":"waiting","last_read_at":null,"interrupts":[]}`) {
		t.Errorf("%s", got.body)
	}
}

func TestStoredInterruptsThatDoNotEncodeAreA500(t *testing.T) {
	h := mount(listed{RunStore: NewMemory(), threads: []Thread{{ID: "a", Status: StatusWaiting, Interrupts: json.RawMessage(`[{`)}}})
	if got := do(t, h, "GET", "/threads/", "alice", ""); got.status != 500 || strings.Contains(got.body, "interrupts") {
		t.Errorf("%d %s", got.status, got.body)
	}
}

func TestTheReadRoute(t *testing.T) {
	h := mount(seeded(t))
	for _, c := range []struct {
		method, path, user string
		status             int
	}{
		{"POST", "/threads/th9/read/", "alice", http.StatusNotFound},
		{"POST", "/threads/nope/read/", "alice", http.StatusNotFound},
		{"POST", "/threads/th9/read/", "bob", http.StatusNoContent},
		{"GET", "/threads/th9/read/", "bob", http.StatusMethodNotAllowed},
		{"POST", "/threads/th9/other/", "bob", http.StatusNotFound},
		{"POST", "/threads/th9/read/x/", "bob", http.StatusNotFound},
	} {
		if got := do(t, h, c.method, c.path, c.user, ""); got.status != c.status {
			t.Errorf("%s %s as %s: %d %s", c.method, c.path, c.user, got.status, got.body)
		}
	}
	if got := do(t, mount(threadsOnly{seeded(t)}), "POST", "/threads/th9/read/", "bob", ""); got.status != http.StatusNotFound {
		t.Errorf("a store with no run status marked a read: %d", got.status)
	}
	failing := listed{RunStore: failingReads{NewMemory()}}
	if got := do(t, mount(failing), "POST", "/threads/th9/read/", "bob", ""); got.status != 500 || strings.Contains(got.body, "disk") {
		t.Errorf("a failing read: %d %s", got.status, got.body)
	}
}

type failingReads struct{ *Memory }

func (failingReads) MarkRead(context.Context, string, string) error { return errors.New("disk full") }
