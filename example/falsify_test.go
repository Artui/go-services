package example

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	services "github.com/Artui/go-services"
)

// brokenResolverOver is resolverOver with the ordering rule broken: it ignores
// the transaction the atomic callback put in the context and always hands back
// the pool.
//
// This is what the kernel's documentation means by "resolving first and running
// the service inside looks identical, passes every happy-path test, and writes
// half the mutation outside the transaction on rollback". It is written out
// here so the claim is executable rather than asserted.
func brokenResolverOver(db *sql.DB) func(context.Context, any) (Deps, error) {
	return func(_ context.Context, principal any) (Deps, error) {
		id, ok := principal.(int64)
		if !ok || id <= 0 {
			return Deps{}, services.ErrPermission
		}
		// Every other field is what resolverOver would have produced. The
		// falsification is only worth something while the two registries differ
		// in the boundary and in nothing else.
		return Deps{DB: db, MemberID: id, Now: time.Now}, nil
	}
}

// TestRollbackAssertionHasTeeth is the falsification, and it is the reason to
// believe TestRollbackLeavesNoOrphanLoan.
//
// A rollback assertion passes trivially against a service that never wrote
// anything, against a database that discarded the write for some unrelated
// reason, and against a transaction that was never opened at all. So the same
// dispatch is run against a registry whose dependencies resolve OUTSIDE the
// boundary, and the orphan row is asserted to appear. If this test ever goes
// green with zero loans, the assertion next door has stopped meaning anything
// and both need re-reading.
//
// Everything else is identical: same schema, same seed, same spec set, same
// input, same error. Only the resolver differs.
func TestRollbackAssertionHasTeeth(t *testing.T) {
	db := newDB(t)
	reg := registryWith(db, brokenResolverOver(db))

	// The borrow fails exactly as it does in the correct registry: book 11 has
	// no copy on the shelf, so the guarded decrement changes no rows.
	_, err := reg.Dispatch(t.Context(), int64(1), "borrow_book", borrowRaw(11))
	if !errors.Is(err, services.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict -- the two registries must fail the same way", err)
	}

	// And the loan row is still there, because the INSERT never entered the
	// transaction that was rolled back.
	if n := countLoans(t, db); n != 1 {
		t.Fatalf("loans = %d, want 1; with deps resolved outside the boundary the "+
			"orphan row must survive, or TestRollbackLeavesNoOrphanLoan proves nothing", n)
	}
}

// The same break, seen on the happy path: nothing fails, so nothing looks
// wrong. This is why the ordering rule needs a test at all -- a suite of
// successful mutations cannot tell the two registries apart.
func TestBrokenBoundaryIsInvisibleWhenNothingFails(t *testing.T) {
	db := newDB(t)
	broken := registryWith(db, brokenResolverOver(db))

	res, err := broken.Dispatch(t.Context(), int64(1), "borrow_book", borrowRaw(10))
	if err != nil {
		t.Fatalf("borrow: %v", err)
	}
	if out := res.Value.(BorrowOut); out.Remaining != 1 {
		t.Errorf("remaining = %d, want 1", out.Remaining)
	}
	if n := countLoans(t, db); n != 1 {
		t.Errorf("loans = %d, want 1", n)
	}
	if n := availableOf(t, db, 10); n != 1 {
		t.Errorf("available = %d, want 1", n)
	}
	// Every assertion above also holds for Registry(db). That is the point.
}
