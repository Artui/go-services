package example

import (
	"context"
	"database/sql"
	"fmt"

	// The pure-Go SQLite driver. No cgo, so the suite is a plain `go test` on
	// every runner and the module's Go floor is decided by ginx rather than by
	// a C toolchain.
	_ "modernc.org/sqlite"
)

// Schema is the whole database. Three tables is enough to make the point that
// one mutation writes two of them, which is what makes a half-applied write
// observable.
const Schema = `
CREATE TABLE members (
    id        INTEGER PRIMARY KEY,
    name      TEXT    NOT NULL,
    -- What Permit reads. It is a column rather than a claim on a token so that
    -- the authorisation decision is a row read inside the same transaction as
    -- the write it gates, which is the property this module exists to show.
    suspended INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE books (
    id        INTEGER PRIMARY KEY,
    title     TEXT    NOT NULL,
    author    TEXT    NOT NULL,
    available INTEGER NOT NULL,
    -- Belt and braces. The borrow path decrements with a guarded UPDATE and
    -- never relies on this firing; if it ever does fire, a caller found a way
    -- past the guard and the transaction is the right place to lose the write.
    CHECK (available >= 0)
);

CREATE TABLE loans (
    id        INTEGER PRIMARY KEY,
    book_id   INTEGER NOT NULL REFERENCES books(id),
    member_id INTEGER NOT NULL REFERENCES members(id)
);
`

// Open returns an empty, migrated database.
//
// The DSN is an in-memory database shared by name. A plain ":memory:" gives
// every pooled connection its own empty database, and the effect of that is
// worth stating precisely because it was measured rather than assumed: with a
// plain DSN, every test in this module still passes EXCEPT the two
// falsifications. Those are the only ones that touch two connections at once --
// a transaction on one, writes on another -- so a broken boundary stops
// reporting an orphan row and starts reporting "no such table", which reads
// like a broken test rather than a proven one.
//
// Which is to say: cache=shared is load-bearing for the falsification
// specifically, and for nothing else here.
func Open(ctx context.Context, name string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A shared in-memory database lives exactly as long as one open connection
	// to it, so letting the pool close its last idle connection would drop
	// every table. This suite does NOT exercise that -- removing both lines
	// leaves it green, because nothing here is idle long enough -- so treat it
	// as a guard for a longer-lived process rather than as something the tests
	// hold in place.
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, Schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Seed writes a small fixed world: two members, one of them suspended, and two
// books, one of which has no copy on the shelf.
//
// Every case this module asserts is reachable from this world without further
// setup, so a test that has to write its own rows is a signal that the world is
// wrong rather than that the test is unusual.
func Seed(ctx context.Context, db *sql.DB) error {
	const rows = `
INSERT INTO members (id, name, suspended) VALUES
    (1, 'Ada',   0),
    (2, 'Grace', 1);
INSERT INTO books (id, title, author, available) VALUES
    (10, 'The Mythical Man-Month', 'Brooks',  2),
    (11, 'Structure and Interpretation', 'Abelson', 0);
`
	_, err := db.ExecContext(ctx, rows)
	return err
}
