package example

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	services "github.com/Artui/go-services"
)

// BorrowIn is what a caller may say. It names a book and nothing else: the
// borrower comes from Deps, so no request can borrow on somebody else's behalf
// by adding a field.
type BorrowIn struct {
	BookID int64 `json:"book_id" jsonschema:"the book to borrow"`
}

// Validate is the layer between the schema and the database. The schema says
// this is an integer; this says which integers are identifiers.
func (in BorrowIn) Validate() error {
	if in.BookID <= 0 {
		return services.Invalid("book_id", "must be a positive identifier")
	}
	return nil
}

// BorrowOut reports the loan and what it left on the shelf.
type BorrowOut struct {
	LoanID    int64 `json:"loan_id"`
	BookID    int64 `json:"book_id"`
	MemberID  int64 `json:"member_id"`
	Remaining int64 `json:"remaining"`
}

// ListIn filters the catalogue. Every field is optional, which is what makes it
// a useful test of three transports: the same absent filter arrives as a
// missing key over MCP, a missing query parameter over HTTP, and neither of
// those is the same as an empty string.
type ListIn struct {
	Author        string `json:"author,omitempty" jsonschema:"match books by this author exactly"`
	AvailableOnly bool   `json:"available_only,omitempty"`
	Limit         int    `json:"limit,omitempty" jsonschema:"at most this many books, up to 100"`
}

// Validate is the ceiling on a page size. The schema cannot express it: a
// jsonschema tag carries a description and no constraints.
func (in ListIn) Validate() error {
	switch {
	case in.Limit < 0:
		return services.Invalid("limit", "must not be negative")
	case in.Limit > 100:
		return services.Invalid("limit", "must be 100 or fewer")
	}
	return nil
}

// Book is one catalogue row.
type Book struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Author    string `json:"author"`
	Available int64  `json:"available"`
}

// ListOut wraps the rows in an object rather than returning a bare array,
// because an object can grow a field and a top-level array cannot.
type ListOut struct {
	Books []Book `json:"books"`
}

// Registry is the spec set, wired to db.
func Registry(db *sql.DB) *services.Registry[Deps] {
	return registryWith(db, resolverOver(db))
}

// registryWith is the same registry with the dependency resolver supplied.
//
// It exists as a seam for exactly one caller: the falsification, which needs to
// build this registry with the boundary deliberately broken. Nothing else
// should use it, which is why it is unexported.
func registryWith(
	db *sql.DB, resolve func(context.Context, any) (Deps, error),
) *services.Registry[Deps] {
	r := services.New(resolve, services.WithAtomic[Deps](atomicOver(db)))

	services.MustRegister(r, services.Spec[Deps, BorrowIn, BorrowOut]{
		Name:        "borrow_book",
		Description: "Lend one copy of a book to the authenticated member.",
		Kind:        services.Mutation,
		Status:      201,
		Permit: []func(services.Ctx[Deps], BorrowIn) error{
			memberInGoodStanding,
		},
		Run: borrow,
	})

	services.MustRegister(r, services.Spec[Deps, ListIn, ListOut]{
		Name:        "list_books",
		Description: "List the catalogue, optionally filtered by author or availability.",
		Kind:        services.Query,
		Run:         listBooks,
	})

	return r
}

// memberInGoodStanding is the authorisation layer, and it reads a row.
//
// Because Permit runs inside the transaction, this SELECT sees the same
// snapshot the write that follows it will use. That is worth having and it is
// worth being explicit that it is not free: a Permit function is a database
// round trip on the transaction's own connection, so it holds the transaction
// open for its duration.
func memberInGoodStanding(ctx services.Ctx[Deps], _ BorrowIn) error {
	var suspended bool
	err := ctx.Deps.DB.QueryRowContext(ctx.Context,
		`SELECT suspended FROM members WHERE id = ?`, ctx.Deps.MemberID).Scan(&suspended)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Not found rather than forbidden. A principal an adapter
		// authenticated but this service has never heard of is a deployment
		// disagreeing with itself, not a caller doing something they may not.
		return fmt.Errorf("%w: no member %d", services.ErrNotFound, ctx.Deps.MemberID)
	case err != nil:
		return err
	case suspended:
		return fmt.Errorf("%w: member %d is suspended", services.ErrPermission, ctx.Deps.MemberID)
	}
	return nil
}

// borrow writes both tables, in the order that makes a broken boundary visible.
//
// The loan is inserted first and the shelf is decremented second, so a refused
// decrement has to take an already-written loan row with it. Written the other
// way round the rollback would still be wrong and nothing would show it.
func borrow(ctx services.Ctx[Deps], in BorrowIn) (BorrowOut, error) {
	var title string
	err := ctx.Deps.DB.QueryRowContext(ctx.Context,
		`SELECT title FROM books WHERE id = ?`, in.BookID).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return BorrowOut{}, fmt.Errorf("%w: no book %d", services.ErrNotFound, in.BookID)
	}
	if err != nil {
		return BorrowOut{}, err
	}

	res, err := ctx.Deps.DB.ExecContext(ctx.Context,
		`INSERT INTO loans (book_id, member_id) VALUES (?, ?)`, in.BookID, ctx.Deps.MemberID)
	if err != nil {
		return BorrowOut{}, err
	}
	loanID, err := res.LastInsertId()
	if err != nil {
		return BorrowOut{}, err
	}

	// The availability test is in the WHERE clause rather than in a SELECT
	// above it, so two borrowers racing for the last copy cannot both pass a
	// check and both decrement. The row count is the answer.
	res, err = ctx.Deps.DB.ExecContext(ctx.Context,
		`UPDATE books SET available = available - 1 WHERE id = ? AND available > 0`, in.BookID)
	if err != nil {
		return BorrowOut{}, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return BorrowOut{}, err
	}
	if changed == 0 {
		// Conflict, not validation: the request was understood and would have
		// been fine a moment ago. Returning it here is what rolls the loan back.
		return BorrowOut{}, fmt.Errorf(
			"%w: no copy of %q is on the shelf", services.ErrConflict, title)
	}

	var remaining int64
	if err := ctx.Deps.DB.QueryRowContext(ctx.Context,
		`SELECT available FROM books WHERE id = ?`, in.BookID).Scan(&remaining); err != nil {
		return BorrowOut{}, err
	}

	return BorrowOut{
		LoanID:    loanID,
		BookID:    in.BookID,
		MemberID:  ctx.Deps.MemberID,
		Remaining: remaining,
	}, nil
}

// listBooks is the read side. It takes no transaction, which is the reason
// Deps.DB is an interface.
func listBooks(ctx services.Ctx[Deps], in ListIn) (ListOut, error) {
	query := `SELECT id, title, author, available FROM books`
	var where []string
	var args []any

	if in.Author != "" {
		where = append(where, `author = ?`)
		args = append(args, in.Author)
	}
	if in.AvailableOnly {
		where = append(where, `available > 0`)
	}
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	// Ordered because three transports comparing results need a stable one, and
	// because a caller given a limit without an order gets an arbitrary subset.
	query += ` ORDER BY id`
	if in.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, in.Limit)
	}

	rows, err := ctx.Deps.DB.QueryContext(ctx.Context, query, args...)
	if err != nil {
		return ListOut{}, err
	}
	defer func() { _ = rows.Close() }()

	// Not nil: a nil slice marshals to null, and a client reading books.length
	// on an empty catalogue would fail on one transport's idea of "no rows".
	out := ListOut{Books: []Book{}}
	for rows.Next() {
		var b Book
		if err := rows.Scan(&b.ID, &b.Title, &b.Author, &b.Available); err != nil {
			return ListOut{}, err
		}
		out.Books = append(out.Books, b)
	}
	return out, rows.Err()
}
