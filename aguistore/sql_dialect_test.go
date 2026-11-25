package aguistore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	services "github.com/Artui/go-services"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// dialectDB is a database a dialect test runs on, named by the variable that
// holds its DSN; CI's service containers set both.
var dialectDB = []struct {
	dialect Dialect
	driver  string
	env     string
}{
	{Postgres, "pgx", "AGUISTORE_POSTGRES_DSN"},
	{MySQL, "mysql", "AGUISTORE_MYSQL_DSN"},
}

// forEachDialect runs fn on a fresh SQLite store and on a store with empty
// tables in each dialect whose DSN is set, skipping the others.
func forEachDialect(t *testing.T, fn func(t *testing.T, s *SQL), opts ...Option) {
	t.Run("sqlite", func(t *testing.T) { fn(t, openSQL(t, filepath.Join(t.TempDir(), "threads.db"), opts...)) })
	for _, d := range dialectDB {
		t.Run(d.dialect.String(), func(t *testing.T) {
			dsn := os.Getenv(d.env)
			if dsn == "" {
				t.Skipf("%s is not set", d.env)
			}
			db, err := sql.Open(d.driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			for _, table := range []string{"aguistore_runs", "aguistore_threads"} {
				if _, err := db.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
					t.Fatal(err)
				}
			}
			s, err := OpenSQL(context.Background(), db, append(opts, WithDialect(d.dialect))...)
			if err != nil {
				t.Fatal(err)
			}
			fn(t, s)
		})
	}
}

func TestPostgresNumbersItsPlaceholders(t *testing.T) {
	const query = `SELECT 1 FROM t WHERE a = ? AND b = ? LIMIT ?`
	if got := Postgres.rebind(query); got != `SELECT 1 FROM t WHERE a = $1 AND b = $2 LIMIT $3` {
		t.Errorf("postgres: %s", got)
	}
	for _, d := range []Dialect{SQLite, MySQL} {
		if got := d.rebind(query); got != query {
			t.Errorf("%s: %s", d, got)
		}
	}
}

func TestEachDialectUpsertsInItsOwnWords(t *testing.T) {
	for d, want := range map[Dialect]string{
		SQLite:   " ON CONFLICT (user_id, thread_id) DO UPDATE SET ",
		Postgres: " ON CONFLICT (user_id, thread_id) DO UPDATE SET ",
		MySQL:    " AS excluded ON DUPLICATE KEY UPDATE ",
	} {
		if got := d.upsert("user_id", "thread_id"); got != want {
			t.Errorf("%s: %q", d, got)
		}
	}
}

func TestEachDialectKeysCompareByteForByte(t *testing.T) {
	for d, want := range map[Dialect]string{
		SQLite:   "user_id TEXT NOT NULL",
		Postgres: `user_id TEXT COLLATE "C" NOT NULL`,
		MySQL:    "user_id VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL",
	} {
		for _, create := range schemas(d) {
			if !strings.Contains(create, want) {
				t.Errorf("%s: %s", d, create)
			}
		}
	}
	if got := []string{SQLite.String(), Postgres.String(), MySQL.String()}; !slices.Equal(got, []string{"sqlite", "postgres", "mysql"}) {
		t.Errorf("names %v", got)
	}
}

func TestWithDialectReachesTheTables(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "threads.db"))
	_, err := OpenSQL(context.Background(), db, WithDialect(Postgres))
	if err == nil || !strings.Contains(err.Error(), "collation") {
		t.Errorf("SQLite took the Postgres tables: %v", err)
	}
}

func TestEveryDialectKeepsAThread(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *SQL) {
		ctx := context.Background()
		mustSave(t, s, "alice", "th1", msg("u1", "user", "First question."))
		if err := s.RenameThread(ctx, "alice", "th1", "Named"); err != nil {
			t.Fatal(err)
		}
		if err := s.RenameThread(ctx, "alice", "th1", "Named"); err != nil {
			t.Errorf("renaming to the same title: %v", err)
		}
		mustSave(t, s, "alice", "th1", msg("u1", "user", "First question."), msg("a1", "assistant", "An answer."))
		got, ok, err := s.LoadThread(ctx, "alice", "th1")
		if err != nil || !ok || len(got) != 2 || got[1].Text() != "An answer." {
			t.Fatalf("loaded %+v %v %v", got, ok, err)
		}
		if threads := list(t, s, "alice", 10); len(threads) != 1 || threads[0].Title != "Named" || threads[0].Preview != "An answer." {
			t.Errorf("listed %+v", threads)
		}
		if err := s.RenameThread(ctx, "alice", "nope", "x"); !errors.Is(err, services.ErrNotFound) {
			t.Errorf("renaming a missing thread: %v", err)
		}
		if err := s.DeleteThread(ctx, "alice", "th1"); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := s.LoadThread(ctx, "alice", "th1"); ok || err != nil {
			t.Errorf("after delete: %v %v", ok, err)
		}
	}, WithClock(newClock().now))
}

func TestEveryDialectOrdersATieByThreadIDByteForByte(t *testing.T) {
	ids := []string{"b", "é", "B", "a", "A"}
	want := []string{"A", "B", "a", "b", "é"}
	tie := WithClock(func() time.Time { return newClock().at })
	memory := NewMemory(tie)
	for _, id := range ids {
		mustSave(t, memory, "alice", id, msg("u1", "user", id))
	}
	if got := threadIDs(list(t, memory, "alice", 10)); !slices.Equal(got, want) {
		t.Fatalf("memory lists %v", got)
	}
	forEachDialect(t, func(t *testing.T, s *SQL) {
		for _, id := range ids {
			mustSave(t, s, "alice", id, msg("u1", "user", id))
		}
		if got := threadIDs(list(t, s, "alice", 10)); !slices.Equal(got, want) {
			t.Errorf("lists %v", got)
		}
	}, tie)
}

func TestEveryDialectKeepsIDsThatDifferInCaseApart(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *SQL) {
		ctx := context.Background()
		mustSave(t, s, "alice", "th", msg("u1", "user", "lower"))
		mustSave(t, s, "Alice", "th", msg("u1", "user", "other user"))
		mustSave(t, s, "alice", "TH", msg("u1", "user", "upper"))
		for _, c := range []struct{ user, thread, text string }{
			{"alice", "th", "lower"}, {"Alice", "th", "other user"}, {"alice", "TH", "upper"},
		} {
			got, ok, err := s.LoadThread(ctx, c.user, c.thread)
			if err != nil || !ok || got[0].Text() != c.text {
				t.Errorf("%s/%s: %+v %v %v", c.user, c.thread, got, ok, err)
			}
		}
	}, WithClock(newClock().now))
}

func TestEveryDialectClaimsAParkOnce(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *SQL) {
		ctx := context.Background()
		mustSave(t, s, "alice", "th1", msg("u1", "user", "Delete it."))
		if ok, err := s.StartRun(ctx, "alice", "th1", "r1", nil); !ok || err != nil {
			t.Fatalf("start: %v %v", ok, err)
		}
		if err := s.FinishRun(ctx, "alice", "th1", "r1", StatusWaiting, "approval",
			json.RawMessage(`[{"id":"i1"}]`)); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkRead(ctx, "alice", "th1"); err != nil {
			t.Fatal(err)
		}
		th := list(t, s, "alice", 1)[0]
		if th.Status != StatusWaiting || th.Reason != "approval" || string(th.Interrupts) != `[{"id":"i1"}]` || th.LastReadAt.IsZero() {
			t.Errorf("parked %+v", th)
		}
		for i, want := range []bool{true, false} {
			if ok, err := s.StartRun(ctx, "alice", "th1", "r2", []string{"i1"}); ok != want || err != nil {
				t.Errorf("claim %d: %v %v", i, ok, err)
			}
		}
		if err := s.MarkRead(ctx, "alice", "nope"); !errors.Is(err, services.ErrNotFound) {
			t.Errorf("marking a missing thread: %v", err)
		}
	}, WithClock(newClock().now))
}

func TestARenameWhoseCountOrCheckFailsIsAnError(t *testing.T) {
	ctx := context.Background()
	for name, s := range map[string]*SQL{
		"count": {db: sql.OpenDB(uncounted{})},
		"check": {db: sql.OpenDB(brokenStream{})},
	} {
		if err := s.RenameThread(ctx, "alice", "th1", "t"); err == nil ||
			errors.Is(err, services.ErrNotFound) || !strings.HasPrefix(err.Error(), "aguistore: renaming the thread:") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func threadIDs(threads []Thread) []string {
	ids := make([]string, 0, len(threads))
	for _, th := range threads {
		ids = append(ids, th.ID)
	}
	return ids
}
