package aguistore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestASQLThreadSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "threads.db")
	ctx := context.Background()
	db := openDB(t, path)
	s, err := OpenSQL(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	mustSave(t, s, "alice", "th1", msg("u1", "user", "Remember me."))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	again := openSQL(t, path)
	got, ok, err := again.LoadThread(ctx, "alice", "th1")
	if err != nil || !ok || len(got) != 1 || got[0].Text() != "Remember me." {
		t.Fatalf("after a restart: %+v %v %v", got, ok, err)
	}
	if threads := list(t, again, "alice", 10); len(threads) != 1 || threads[0].Title != "Remember me." {
		t.Errorf("listed %+v", threads)
	}
}

func TestSQLErrorsAreReported(t *testing.T) {
	ctx := context.Background()
	s := openSQL(t, filepath.Join(t.TempDir(), "threads.db"))
	if _, err := s.db.ExecContext(ctx, `INSERT INTO aguistore_threads VALUES ('alice', 'bad', 't', 'p', '{', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.LoadThread(ctx, "alice", "bad"); ok || err == nil || !strings.Contains(err.Error(), "decoding the thread") {
		t.Errorf("a corrupt row loaded: %v %v", ok, err)
	}
	if _, err := s.ListThreads(ctx, "alice", 10); err == nil || !strings.Contains(err.Error(), "listing threads") {
		t.Errorf("a corrupt row listed: %v", err)
	}

	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"load":   func() error { _, _, err := s.LoadThread(ctx, "a", "b"); return err }(),
		"save":   s.SaveThread(ctx, "a", "b", nil),
		"list":   func() error { _, err := s.ListThreads(ctx, "a", 1); return err }(),
		"rename": s.RenameThread(ctx, "a", "b", "t"),
		"delete": s.DeleteThread(ctx, "a", "b"),
	} {
		if err == nil || !strings.HasPrefix(err.Error(), "aguistore: ") {
			t.Errorf("%s on a closed database: %v", name, err)
		}
	}
	if _, err := OpenSQL(ctx, s.db); err == nil || !strings.HasPrefix(err.Error(), "aguistore: creating the table") {
		t.Errorf("open on a closed database: %v", err)
	}
}
