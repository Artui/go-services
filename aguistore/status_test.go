package aguistore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	services "github.com/Artui/go-services"
)

var (
	_ RunStore = (*Memory)(nil)
	_ RunStore = (*SQL)(nil)
)

// parked is RUN_FINISHED's interrupts as aguiadk writes them for an approval,
// with a number past 2^53 and keys out of order, so a decode would change it.
const parked = `[{"id":"int-c1","reason":"tool_call","toolCallId":"c1",` +
	`"metadata":{"kind":"approval","destructive":true,"n":9007199254740993}}]`

func eachRuns(t *testing.T, fn func(t *testing.T, s RunStore)) {
	each(t, func(t *testing.T, s Store) { fn(t, s.(RunStore)) })
}

func row(t *testing.T, s Store, user, thread string) Thread {
	t.Helper()
	for _, th := range list(t, s, user, 100) {
		if th.ID == thread {
			return th
		}
	}
	t.Fatalf("%s has no thread %s", user, thread)
	return Thread{}
}

func start(t *testing.T, s RunStore, run string, resumes ...string) bool {
	t.Helper()
	ok, err := s.StartRun(context.Background(), "alice", "th1", run, resumes)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func finish(t *testing.T, s RunStore, run, status, reason, interrupts string) {
	t.Helper()
	var raw json.RawMessage
	if interrupts != "" {
		raw = json.RawMessage(interrupts)
	}
	if err := s.FinishRun(context.Background(), "alice", "th1", run, status, reason, raw); err != nil {
		t.Fatal(err)
	}
}

func TestAThreadsRunStatusFollowsItsRuns(t *testing.T) {
	eachRuns(t, func(t *testing.T, s RunStore) {
		mustSave(t, s, "alice", "th1", msg("u1", "user", "Delete the book."))
		if got := row(t, s, "alice", "th1"); got.Status != "" || got.Interrupts != nil || !got.LastReadAt.IsZero() {
			t.Errorf("before any run: %+v", got)
		}
		start(t, s, "r1")
		if got := row(t, s, "alice", "th1"); got.Status != StatusRunning {
			t.Errorf("while r1 runs: %+v", got)
		}
		finish(t, s, "r1", StatusWaiting, "", parked)
		got := row(t, s, "alice", "th1")
		if got.Status != StatusWaiting || string(got.Interrupts) != parked {
			t.Errorf("parked: %q %s", got.Status, got.Interrupts)
		}
		start(t, s, "r2")
		finish(t, s, "r2", StatusFailed, "error", "")
		if got := row(t, s, "alice", "th1"); got.Status != StatusFailed || got.Reason != "error" || got.Interrupts != nil {
			t.Errorf("failed: %+v", got)
		}
		start(t, s, "r3")
		finish(t, s, "r3", StatusIdle, "", "")
		if got := row(t, s, "alice", "th1"); got.Status != StatusIdle || got.Reason != "" {
			t.Errorf("idle: %+v", got)
		}
	})
}

func TestAResumeClaimsTheParkOnce(t *testing.T) {
	eachRuns(t, func(t *testing.T, s RunStore) {
		mustSave(t, s, "alice", "th1", msg("u1", "user", "Delete the book."))
		if start(t, s, "r0", "int-c1") {
			t.Error("a thread that never parked was claimed")
		}
		start(t, s, "r1")
		if start(t, s, "r1b", "int-c1") {
			t.Error("a running thread was claimed")
		}
		finish(t, s, "r1", StatusWaiting, "", parked)
		if start(t, s, "r2", "int-other") || start(t, s, "r2", "int-c1", "int-other") {
			t.Error("an answer to an interrupt the thread is not parked on was claimed")
		}
		if !start(t, s, "r2", "int-c1") {
			t.Fatal("the resume lost its claim")
		}
		if start(t, s, "r3", "int-c1") {
			t.Error("the same answer was claimed twice")
		}
		// r1 is long replaced; its late finish must not reopen the park.
		finish(t, s, "r1", StatusWaiting, "", parked)
		if got := row(t, s, "alice", "th1"); got.Status != StatusRunning {
			t.Errorf("a stale finish landed: %+v", got)
		}
	})
}

func TestConcurrentResumesOfOneParkHaveOneWinner(t *testing.T) {
	stores := map[string]RunStore{
		"memory": NewMemory(),
		"sql":    openSQL(t, filepath.Join(t.TempDir(), "threads.db")+"?_pragma=busy_timeout(10000)"),
	}
	for name, s := range stores {
		t.Run(name, func(t *testing.T) {
			mustSave(t, s, "alice", "th1", msg("u1", "user", "Delete the book."))
			start(t, s, "r1")
			finish(t, s, "r1", StatusWaiting, "", parked)
			var (
				wg   sync.WaitGroup
				wins atomic.Int32
			)
			for i := range 8 {
				wg.Go(func() {
					ok, err := s.StartRun(context.Background(), "alice", "th1", "r2-"+string(rune('a'+i)), []string{"int-c1"})
					if err != nil {
						t.Error(err)
					}
					if ok {
						wins.Add(1)
					}
				})
			}
			wg.Wait()
			if wins.Load() != 1 {
				t.Errorf("%d resumes won", wins.Load())
			}
		})
	}
}

func TestMarkReadStampsOnlyTheUsersThread(t *testing.T) {
	eachRuns(t, func(t *testing.T, s RunStore) {
		ctx := context.Background()
		mustSave(t, s, "alice", "th1", msg("u1", "user", "Hi."))
		for _, user := range []string{"alice", "bob"} {
			if err := s.MarkRead(ctx, user, map[string]string{"alice": "nope", "bob": "th1"}[user]); !errors.Is(err, services.ErrNotFound) {
				t.Errorf("%s marked a thread it lacks: %v", user, err)
			}
		}
		if err := s.MarkRead(ctx, "alice", "th1"); err != nil {
			t.Fatal(err)
		}
		got := row(t, s, "alice", "th1")
		if got.LastReadAt.IsZero() || !got.LastReadAt.After(got.UpdatedAt) {
			t.Errorf("read at %v, updated at %v", got.LastReadAt, got.UpdatedAt)
		}
		start(t, s, "r1")
		if again := row(t, s, "alice", "th1"); !again.LastReadAt.Equal(got.LastReadAt) {
			t.Errorf("a run moved the read stamp: %v", again.LastReadAt)
		}
	})
}

func TestDeletingAThreadForgetsItsStatus(t *testing.T) {
	eachRuns(t, func(t *testing.T, s RunStore) {
		mustSave(t, s, "alice", "th1", msg("u1", "user", "Hi."))
		start(t, s, "r1")
		finish(t, s, "r1", StatusWaiting, "", parked)
		if err := s.DeleteThread(context.Background(), "alice", "th1"); err != nil {
			t.Fatal(err)
		}
		mustSave(t, s, "alice", "th1", msg("u1", "user", "Hi again."))
		if got := row(t, s, "alice", "th1"); got.Status != "" || got.Interrupts != nil {
			t.Errorf("the deleted thread's park came back: %+v", got)
		}
	})
}

func TestARunCannotEndInAStatusNoRunEndsIn(t *testing.T) {
	eachRuns(t, func(t *testing.T, s RunStore) {
		ctx := context.Background()
		for _, c := range []struct{ status, interrupts, want string }{
			{StatusRunning, "", "cannot end"},
			{"", "", "cannot end"},
			{StatusIdle, parked, "parks on no interrupts"},
			{StatusFailed, parked, "parks on no interrupts"},
			{StatusWaiting, "", "JSON array"},
			{StatusWaiting, `{"id":"x"}`, "JSON array"},
			{StatusWaiting, `[{`, "JSON array"},
		} {
			var raw json.RawMessage
			if c.interrupts != "" {
				raw = json.RawMessage(c.interrupts)
			}
			if err := s.FinishRun(ctx, "alice", "th1", "r1", c.status, "", raw); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%q %s: %v", c.status, c.interrupts, err)
			}
		}
	})
}

func TestAParkWhoseInterruptsDoNotDecodeIsNotClaimed(t *testing.T) {
	if claimable(StatusWaiting, []byte(`{`), []string{"int-c1"}) || claimable(StatusWaiting, []byte(parked), nil) {
		t.Error("claimed")
	}
}

func TestAVersionOneDatabaseGainsTheRunsTable(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, filepath.Join(t.TempDir(), "threads.db"))
	if _, err := db.ExecContext(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO aguistore_threads VALUES ('alice', 'th1', 'Old', 'p', '[]', 1)`); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSQL(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if got := row(t, s, "alice", "th1"); got.Title != "Old" || got.Status != "" {
		t.Errorf("the old row: %+v", got)
	}
	start(t, s, "r1")
	if got := row(t, s, "alice", "th1"); got.Status != StatusRunning {
		t.Errorf("the old row does not record runs: %+v", got)
	}
}

func TestSQLRunErrorsAreReported(t *testing.T) {
	ctx := context.Background()
	s := openSQL(t, filepath.Join(t.TempDir(), "threads.db"))
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"start":  func() error { _, err := s.StartRun(ctx, "a", "b", "r", nil); return err }(),
		"claim":  func() error { _, err := s.StartRun(ctx, "a", "b", "r", []string{"i"}); return err }(),
		"finish": s.FinishRun(ctx, "a", "b", "r", StatusIdle, "", nil),
		"take":   func() error { _, err := s.take(ctx, "a", "b", "r", "p"); return err }(),
		"read":   s.MarkRead(ctx, "a", "b"),
	} {
		if err == nil || !strings.HasPrefix(err.Error(), "aguistore: ") {
			t.Errorf("%s on a closed database: %v", name, err)
		}
	}
}

func TestAClaimReadBeforeAnotherWonIsLost(t *testing.T) {
	ctx := context.Background()
	s := openSQL(t, filepath.Join(t.TempDir(), "threads.db"))
	mustSave(t, s, "alice", "th1", msg("u1", "user", "Delete the book."))
	start(t, s, "r1")
	finish(t, s, "r1", StatusWaiting, "", parked)
	if !start(t, s, "r2", "int-c1") {
		t.Fatal("the first resume lost")
	}
	if won, err := s.take(ctx, "alice", "th1", "r3", "r2"); won || err != nil {
		t.Errorf("a claim on a running thread won: %v %v", won, err)
	}
	finish(t, s, "r2", StatusWaiting, "", parked)
	// A second resume that read the park while r1 still held it updates now.
	if won, err := s.take(ctx, "alice", "th1", "r3", "r1"); won || err != nil {
		t.Errorf("a stale claim won: %v %v", won, err)
	}
	if won, err := s.take(ctx, "alice", "th1", "r3", "r2"); !won || err != nil {
		t.Errorf("a current claim lost: %v %v", won, err)
	}
}
