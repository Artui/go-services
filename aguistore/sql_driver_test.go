package aguistore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
)

// brokenStream is a driver whose queries answer one row and then fail, as a
// connection that drops mid-result does.
type brokenStream struct{}

func (brokenStream) Connect(context.Context) (driver.Conn, error) { return brokenStream{}, nil }
func (brokenStream) Driver() driver.Driver                        { return nil }
func (brokenStream) Prepare(string) (driver.Stmt, error)          { return brokenStream{}, nil }
func (brokenStream) Close() error                                 { return nil }
func (brokenStream) Begin() (driver.Tx, error)                    { return nil, errors.ErrUnsupported }
func (brokenStream) NumInput() int                                { return -1 }
func (brokenStream) Exec([]driver.Value) (driver.Result, error)   { return driver.RowsAffected(0), nil }
func (brokenStream) Query([]driver.Value) (driver.Rows, error)    { return &brokenRows{}, nil }

type brokenRows struct{ sent bool }

func (*brokenRows) Columns() []string {
	return []string{"thread_id", "title", "preview", "updated_at", "status", "reason", "interrupts", "last_read_at"}
}
func (*brokenRows) Close() error { return nil }
func (r *brokenRows) Next(dest []driver.Value) error {
	if r.sent {
		return io.ErrUnexpectedEOF
	}
	r.sent = true
	copy(dest, []driver.Value{"th1", "t", "p", int64(0), "", "", "", nil})
	return nil
}

func TestAListThatFailsMidStreamIsAnError(t *testing.T) {
	s := &SQL{db: sql.OpenDB(brokenStream{})}
	got, err := s.ListThreads(context.Background(), "alice", 10)
	if !errors.Is(err, io.ErrUnexpectedEOF) || !strings.HasPrefix(err.Error(), "aguistore: listing threads:") || got != nil {
		t.Errorf("%+v %v", got, err)
	}
}
