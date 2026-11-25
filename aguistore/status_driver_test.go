package aguistore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// uncounted is a driver holding one parked run whose updates cannot say how
// many rows they changed, as some drivers cannot.
type uncounted struct{}

func (uncounted) Connect(context.Context) (driver.Conn, error) { return uncounted{}, nil }
func (uncounted) Driver() driver.Driver                        { return nil }
func (uncounted) Prepare(string) (driver.Stmt, error)          { return uncounted{}, nil }
func (uncounted) Close() error                                 { return nil }
func (uncounted) Begin() (driver.Tx, error)                    { return nil, errors.ErrUnsupported }
func (uncounted) NumInput() int                                { return -1 }
func (uncounted) Exec([]driver.Value) (driver.Result, error)   { return uncountedResult{}, nil }
func (uncounted) Query([]driver.Value) (driver.Rows, error)    { return &parkedRow{}, nil }

type uncountedResult struct{}

func (uncountedResult) LastInsertId() (int64, error) { return 0, errors.ErrUnsupported }
func (uncountedResult) RowsAffected() (int64, error) { return 0, io.ErrUnexpectedEOF }

type parkedRow struct{ sent bool }

func (*parkedRow) Columns() []string { return []string{"run_id", "status", "interrupts"} }
func (*parkedRow) Close() error      { return nil }
func (r *parkedRow) Next(dest []driver.Value) error {
	if r.sent {
		return io.EOF
	}
	r.sent = true
	copy(dest, []driver.Value{"r1", StatusWaiting, parked})
	return nil
}

func TestAClaimWhoseCountFailsIsAnError(t *testing.T) {
	s := &SQL{db: sql.OpenDB(uncounted{})}
	_, ok, err := s.StartRun(context.Background(), "alice", "th1", "r2", []string{"int-c1"})
	if ok || !errors.Is(err, io.ErrUnexpectedEOF) || !strings.HasPrefix(err.Error(), "aguistore: claiming the run:") {
		t.Errorf("%v %v", ok, err)
	}
}

func TestAReadStampThatFailsToWriteIsAnError(t *testing.T) {
	ctx := context.Background()
	s := openSQL(t, filepath.Join(t.TempDir(), "threads.db"))
	mustSave(t, s, "alice", "th1", msg("u1", "user", "Hi."))
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER refuse BEFORE INSERT ON aguistore_runs
		BEGIN SELECT RAISE(FAIL, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRead(ctx, "alice", "th1"); err == nil || !strings.HasPrefix(err.Error(), "aguistore: marking the thread read:") {
		t.Errorf("%v", err)
	}
}
