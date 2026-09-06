package example

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	services "github.com/Artui/go-services"
)

// Queryer is the half of database/sql that both *sql.DB and *sql.Tx implement.
//
// The plan for this module said "Deps carries a *sql.Tx". It cannot, and
// finding out why is the first thing writing it taught: only an atomic entry
// runs inside the transaction callback, so a Query spec resolves its
// dependencies with a context that has no transaction in it. A concrete *sql.Tx
// field would be nil for every read operation in the registry.
//
// So the field is an interface, and which implementation lands in it is the
// whole subject of resolver below.
type Queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Deps is what every service and every Permit function in this module receives.
//
// Identity is a field here rather than a parameter anywhere, which is the
// kernel's decision that there is no Actor type: an adapter authenticates
// whoever it can and hands the registry an opaque principal, and this package
// is the one place that says what a principal actually is.
type Deps struct {
	// DB is transactional for a Mutation and pooled for a Query. Nothing
	// downstream has to know which, and nothing downstream may check.
	DB Queryer

	// MemberID is the authenticated borrower. A borrow takes no member in its
	// input for this reason: a caller may say which book, never which member.
	MemberID int64

	// Now is the clock. It is a dependency rather than a call to time.Now
	// inside the services because two of this module's answers are computed
	// from it -- when a loan falls due, and what a late one has cost so far --
	// and a test that cannot fix the clock can only assert those approximately.
	//
	// It is on Deps rather than on the spec input for the same reason MemberID
	// is: a caller may not say what time it is.
	Now func() time.Time
}

// txKey carries the transaction from the atomic callback to the resolver. It is
// unexported and of an unexported type, so nothing outside this package can put
// a value under it or read one out.
type txKey struct{}

// atomicOver is the callback the registry runs a Mutation inside.
//
// It receives a context and is expected to return one that is transactional --
// which here means stashing the *sql.Tx where resolver can find it. That
// handoff through the context is not incidental: it is the only way the
// resolver can hand a service a transactional handle, and it only works because
// the registry resolves dependencies after this function has opened the
// transaction rather than before.
func atomicOver(db *sql.DB) func(context.Context, func(context.Context) error) error {
	return func(ctx context.Context, run func(context.Context) error) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		// Rollback after a successful Commit is a documented no-op, so this
		// covers the error path and the panic path without a flag to track.
		defer func() { _ = tx.Rollback() }()

		if err := run(context.WithValue(ctx, txKey{}, tx)); err != nil {
			return err
		}
		return tx.Commit()
	}
}

// resolverOver turns the adapter's opaque principal into Deps.
//
// The two lines choosing between tx and db are the entire ordering rule seen
// from the consumer's side. If the registry resolved dependencies before
// opening the transaction, this function would run with a context that has no
// transaction in it and would fall through to db -- for mutations as well as
// reads. Every test would still pass, because the writes would still happen;
// they would simply happen outside the boundary that is supposed to be able to
// undo them. brokenResolverOver in the test file is that mistake, written down
// and asserted against.
func resolverOver(db *sql.DB) func(context.Context, any) (Deps, error) {
	return resolverAt(db, time.Now)
}

// resolverAt is resolverOver with the clock supplied, which is what lets a test
// assert a due date and a fine as exact values rather than as ranges.
func resolverAt(db *sql.DB, now func() time.Time) func(context.Context, any) (Deps, error) {
	return func(ctx context.Context, principal any) (Deps, error) {
		id, ok := principal.(int64)
		if !ok || id <= 0 {
			// Not ErrPermission with a message naming the type: an adapter that
			// authenticated nobody and one that authenticated a stranger are
			// the same answer to a client, and the difference belongs in a log.
			return Deps{}, fmt.Errorf("%w: no member is authenticated", services.ErrPermission)
		}

		var q Queryer = db
		if tx, ok := ctx.Value(txKey{}).(*sql.Tx); ok {
			q = tx
		}
		return Deps{DB: q, MemberID: id, Now: now}, nil
	}
}
