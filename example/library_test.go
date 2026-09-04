package example

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	services "github.com/Artui/go-services"
)

// Each test gets its own shared-cache database. The name is a counter rather
// than t.Name() because a subtest's name carries a slash and the DSN is a URI.
var dbSeq atomic.Int64

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(t.Context(), fmt.Sprintf("example%d", dbSeq.Add(1)))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Seed(t.Context(), db); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return db
}

// countLoans is the assertion this whole module is built around: how many loan
// rows survived.
func countLoans(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM loans`).Scan(&n); err != nil {
		t.Fatalf("count loans: %v", err)
	}
	return n
}

func availableOf(t *testing.T, db *sql.DB, id int64) int64 {
	t.Helper()
	var n int64
	err := db.QueryRowContext(t.Context(), `SELECT available FROM books WHERE id = ?`, id).Scan(&n)
	if err != nil {
		t.Fatalf("available: %v", err)
	}
	return n
}

func borrowRaw(id int64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"book_id":%d}`, id))
}

// Ada borrows a book with copies on the shelf. Both tables move, together.
func TestBorrowCommitsBothTables(t *testing.T) {
	db := newDB(t)
	reg := Registry(db)

	res, err := reg.Dispatch(t.Context(), int64(1), "borrow_book", borrowRaw(10))
	if err != nil {
		t.Fatalf("borrow: %v", err)
	}

	out, ok := res.Value.(BorrowOut)
	if !ok {
		t.Fatalf("value is %T, want BorrowOut", res.Value)
	}
	if out.Remaining != 1 {
		t.Errorf("remaining = %d, want 1", out.Remaining)
	}
	if res.Status != 201 {
		t.Errorf("status = %d, want 201", res.Status)
	}
	if n := countLoans(t, db); n != 1 {
		t.Errorf("loans = %d, want 1", n)
	}
	if n := availableOf(t, db, 10); n != 1 {
		t.Errorf("available = %d, want 1", n)
	}
}

// The one this module exists for. Book 11 has no copy on the shelf, so the loan
// row is written and then the decrement is refused -- and the loan has to go
// with it.
//
// TestRollbackAssertionHasTeeth is the proof that this test can fail.
func TestRollbackLeavesNoOrphanLoan(t *testing.T) {
	db := newDB(t)
	reg := Registry(db)

	_, err := reg.Dispatch(t.Context(), int64(1), "borrow_book", borrowRaw(11))
	if !errors.Is(err, services.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}

	if n := countLoans(t, db); n != 0 {
		t.Errorf("loans = %d, want 0: the insert escaped the transaction", n)
	}
	if n := availableOf(t, db, 11); n != 0 {
		t.Errorf("available = %d, want 0", n)
	}
}

// Permit refuses before anything is written, so there is nothing to roll back.
func TestSuspendedMemberIsRefused(t *testing.T) {
	db := newDB(t)
	reg := Registry(db)

	_, err := reg.Dispatch(t.Context(), int64(2), "borrow_book", borrowRaw(10))
	if !errors.Is(err, services.ErrPermission) {
		t.Fatalf("err = %v, want ErrPermission", err)
	}
	if n := countLoans(t, db); n != 0 {
		t.Errorf("loans = %d, want 0", n)
	}
	if n := availableOf(t, db, 10); n != 2 {
		t.Errorf("available = %d, want 2", n)
	}
}

func TestUnknownBookIsNotFound(t *testing.T) {
	db := newDB(t)
	reg := Registry(db)

	_, err := reg.Dispatch(t.Context(), int64(1), "borrow_book", borrowRaw(999))
	if !errors.Is(err, services.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if n := countLoans(t, db); n != 0 {
		t.Errorf("loans = %d, want 0", n)
	}
}

// An unauthenticated caller never reaches a service, because the resolver
// refuses first -- and the resolver runs inside the transaction, so this also
// covers a boundary opened for a call that turns out to be forbidden.
func TestUnauthenticatedIsRefused(t *testing.T) {
	db := newDB(t)
	reg := Registry(db)

	_, err := reg.Dispatch(t.Context(), nil, "borrow_book", borrowRaw(10))
	if !errors.Is(err, services.ErrPermission) {
		t.Fatalf("err = %v, want ErrPermission", err)
	}
	if n := countLoans(t, db); n != 0 {
		t.Errorf("loans = %d, want 0", n)
	}
}

func TestValidationRefusesBeforeAnyTransaction(t *testing.T) {
	db := newDB(t)
	reg := Registry(db)

	var invalid *services.ValidationError
	_, err := reg.Dispatch(t.Context(), int64(1), "borrow_book", borrowRaw(0))
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want ValidationError", err)
	}
	if got := invalid.FieldMap()["book_id"]; len(got) != 1 {
		t.Errorf("field messages = %v, want one for book_id", invalid.FieldMap())
	}
}

func TestListFilters(t *testing.T) {
	db := newDB(t)
	reg := Registry(db)

	cases := map[string]struct {
		raw  string
		want []int64
	}{
		"no filter":      {`{}`, []int64{10, 11}},
		"by author":      {`{"author":"Brooks"}`, []int64{10}},
		"available":      {`{"available_only":true}`, []int64{10}},
		"limited":        {`{"limit":1}`, []int64{10}},
		"no such author": {`{"author":"nobody"}`, nil},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := reg.Dispatch(t.Context(), int64(1), "list_books", json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			out := res.Value.(ListOut)
			var got []int64
			for _, b := range out.Books {
				got = append(got, b.ID)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("ids = %v, want %v", got, tc.want)
			}
			// Never nil, on every path: a null here is a client-side crash.
			if out.Books == nil {
				t.Error("books is nil, which marshals to null")
			}
		})
	}
}

func TestLimitCeilingIsValidated(t *testing.T) {
	db := newDB(t)
	reg := Registry(db)

	var invalid *services.ValidationError
	_, err := reg.Dispatch(t.Context(), int64(1), "list_books", json.RawMessage(`{"limit":101}`))
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want ValidationError", err)
	}
}

// The two-mode behaviour that forced Deps.DB to be an interface, asserted
// rather than described: a Mutation is handed the transaction, a Query is
// handed the pool, and Permit is handed the same handle its service will use.
func TestMutationGetsTheTransactionAndQueryDoesNot(t *testing.T) {
	db := newDB(t)

	var permitDB, runDB, queryDB Queryer
	r := services.New(resolverOver(db), services.WithAtomic[Deps](atomicOver(db)))

	services.MustRegister(r, services.Spec[Deps, BorrowIn, BorrowOut]{
		Name: "probe_mutation", Kind: services.Mutation,
		Permit: []func(services.Ctx[Deps], BorrowIn) error{
			func(ctx services.Ctx[Deps], _ BorrowIn) error { permitDB = ctx.Deps.DB; return nil },
		},
		Run: func(ctx services.Ctx[Deps], _ BorrowIn) (BorrowOut, error) {
			runDB = ctx.Deps.DB
			return BorrowOut{}, nil
		},
	})
	services.MustRegister(r, services.Spec[Deps, ListIn, ListOut]{
		Name: "probe_query", Kind: services.Query,
		Run: func(ctx services.Ctx[Deps], _ ListIn) (ListOut, error) {
			queryDB = ctx.Deps.DB
			return ListOut{}, nil
		},
	})

	if _, err := r.Dispatch(t.Context(), int64(1), "probe_mutation", borrowRaw(10)); err != nil {
		t.Fatalf("mutation: %v", err)
	}
	if _, err := r.Dispatch(t.Context(), int64(1), "probe_query", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("query: %v", err)
	}

	if _, ok := permitDB.(*sql.Tx); !ok {
		t.Errorf("Permit got %T, want *sql.Tx", permitDB)
	}
	if permitDB != runDB {
		t.Error("Permit and Run were handed different handles")
	}
	if _, ok := queryDB.(*sql.DB); !ok {
		t.Errorf("Query got %T, want *sql.DB -- which is why Deps.DB is an interface", queryDB)
	}
}

// A compile-time reminder that both halves of the interface are real.
var (
	_ Queryer = (*sql.DB)(nil)
	_ Queryer = (*sql.Tx)(nil)
)
