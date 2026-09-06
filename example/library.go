package example

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

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

// BorrowOut reports the loan, when it falls due, and what it left on the shelf.
type BorrowOut struct {
	LoanID    int64      `json:"loan_id"`
	BookID    int64      `json:"book_id"`
	MemberID  int64      `json:"member_id"`
	Remaining int64      `json:"remaining"`
	Status    LoanStatus `json:"status"`
	DueAt     time.Time  `json:"due_at" jsonschema:"when the book must be back"`
}

// ListIn filters the catalogue. Every field is optional, which is what makes it
// a useful test of three transports: the same absent filter arrives as a
// missing key over MCP, a missing query parameter over HTTP, and neither of
// those is the same as an empty string.
type ListIn struct {
	Author        string `json:"author,omitempty" jsonschema:"match books by this author exactly"`
	AvailableOnly bool   `json:"available_only,omitempty"`
	Limit         int    `json:"limit,omitempty" jsonschema:"at most this many books, up to 100"`

	// Cursor is the next_cursor of a previous answer, passed back unchanged.
	Cursor string `json:"cursor,omitempty" jsonschema:"the next_cursor of a previous answer, passed back unchanged"`
}

// Validate is the ceiling on a page size. The schema cannot express it: a
// jsonschema tag carries a description and no constraints.
//
// The cursor is NOT checked here, and that is worth reading rather than
// inferring. Validate returns an error and nothing else, so a check that
// produces a value -- which id to page from -- has nowhere to put it. Doing it
// here would mean decoding the token twice, so listBooks decodes it once and
// returns services.Invalid itself; the kernel maps that to the same answer
// wherever it is raised.
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

	// NextCursor is the token that fetches the page after this one. It is
	// absent on the last page.
	//
	// It is an opaque token on purpose: what it encodes is this service's
	// business and changing it must not break a caller holding one. The
	// description says so in the field's own words, which the transports that
	// publish an output schema carry to whoever reads the payload -- and which
	// is as far as any declaration can go, since nothing on either side can
	// stop a reader looking at a token anyway.
	NextCursor string `json:"next_cursor,omitempty" jsonschema:"an opaque token; pass it back as cursor to fetch the next page, and do not show it to a person or try to read it"`
}

// LoanStatus is where a loan stands.
//
// A string rather than an int, so the value on the wire is the value in the
// code and a reader of either sees the same word.
type LoanStatus string

// The three states a loan can be in. There is no fourth: a book is out, out
// and late, or back.
const (
	StatusOnLoan   LoanStatus = "on_loan"
	StatusOverdue  LoanStatus = "overdue"
	StatusReturned LoanStatus = "returned"
)

// JSONSchema declares the three values, so what a client is told about this
// field is a list it can check rather than a sentence it has to read.
//
// Reflection cannot produce it: a named string type reflects to "string" and
// the constants above are invisible to it. A jsonschema struct tag cannot
// either -- it carries a description and nothing else. The kernel's SchemaFor
// is the remaining channel, and it works on an OUTPUT type as well as an input
// one, which is the only way anything machine-readable can be said about a
// field a caller only ever reads.
func (LoanStatus) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{
		Type:        "string",
		Description: "where the loan stands",
		Enum:        []any{StatusOnLoan, StatusOverdue, StatusReturned},
	}, nil
}

// The lending rules, in one place because they are the numbers a librarian
// would argue about and the code should not scatter them.
const (
	// LoanPeriod is how long a book may be kept before it falls due.
	LoanPeriod = 14 * 24 * time.Hour

	// FinePerDayCents is charged for each whole day past the due date, and
	// MaxFineCents is where it stops. Minor units, because money in a float is
	// a bug waiting for a rounding error.
	FinePerDayCents = 25
	MaxFineCents    = 1000
)

// Loan is one row of a member's own lending history.
type Loan struct {
	LoanID int64  `json:"loan_id"`
	BookID int64  `json:"book_id"`
	Title  string `json:"title"`

	Status LoanStatus `json:"status"`
	DueAt  time.Time  `json:"due_at" jsonschema:"when the book must be back"`

	// FineCents is what this loan has cost so far, in cents. It stops growing
	// when the book comes back.
	FineCents int64 `json:"fine_cents" jsonschema:"the fine owed on this loan, in cents"`
}

// ListLoansIn scopes a member's own history. There is no member field, for the
// same reason BorrowIn has none.
type ListLoansIn struct {
	IncludeReturned bool `json:"include_returned,omitempty" jsonschema:"also list loans that have already been returned"`
}

// ListLoansOut wraps the rows, for the reason ListOut does.
type ListLoansOut struct {
	Loans []Loan `json:"loans"`
}

// assess reports where a loan stands and what it has cost.
//
// The two answers are computed together because they come from the same pair of
// dates, and splitting them would mean deciding twice which end of the loan the
// fine is measured to.
func assess(due time.Time, returned *time.Time, now time.Time) (LoanStatus, int64) {
	status := StatusOnLoan
	end := now
	switch {
	case returned != nil:
		status, end = StatusReturned, *returned
	case now.After(due):
		status = StatusOverdue
	}

	late := end.Sub(due)
	if late <= 0 {
		return status, 0
	}
	// Whole days, not started ones. Either rule is defensible and a library
	// would pick one; what matters here is that the number is derived from the
	// row rather than stored, so it cannot go stale.
	fine := int64(late/(24*time.Hour)) * FinePerDayCents
	if fine > MaxFineCents {
		fine = MaxFineCents
	}
	return status, fine
}

// cursorPrefix is what an encoded cursor holds in front of the id it pages from.
//
// Base64 is an encoding and not a lock, so a caller who wants to look can: the
// token says "opaque" by convention, and the convention is what stops a
// well-behaved caller building one rather than what stops a determined one. It
// buys the freedom to change what is encoded without breaking anybody who kept
// their side of it, and nothing more than that.
const cursorPrefix = "after:"

func encodeCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte(cursorPrefix + strconv.FormatInt(id, 10)))
}

// decodeCursor turns a token back into the id it pages from.
//
// Every failure is one answer. A caller cannot act on the difference between
// "that is not base64" and "that is base64 of something else", and spelling the
// difference out would describe the encoding to whoever is probing it.
func decodeCursor(cursor string) (int64, error) {
	refuse := services.Invalid("cursor", "is not a cursor this service issued")

	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, refuse
	}
	rest, found := strings.CutPrefix(string(raw), cursorPrefix)
	if !found {
		return 0, refuse
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0, refuse
	}
	return id, nil
}

// Registry is the spec set, wired to db.
func Registry(db *sql.DB) *services.Registry[Deps] {
	return registryWith(db, resolverOver(db))
}

// registryWith is the same registry with the dependency resolver supplied.
//
// It exists as a seam for two callers, both of them tests. The falsification
// needs this registry with the transaction boundary deliberately broken, and
// the payload tests need it with the clock stopped, because a due date and a
// fine cannot be asserted as exact bytes against time.Now. Nothing an
// application does should use it, which is why it is unexported.
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

	services.MustRegister(r, services.Spec[Deps, ListLoansIn, ListLoansOut]{
		Name:        "list_loans",
		Description: "List the authenticated member's own loans, with what each one owes.",
		Kind:        services.Query,
		Run:         listLoans,
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

	// Truncated to the second on the way in, so what this returns is what the
	// row holds: a client reading the due date back gets the same timestamp
	// rather than one a fraction adrift.
	dueAt := time.Unix(ctx.Deps.Now().Add(LoanPeriod).Unix(), 0).UTC()

	res, err := ctx.Deps.DB.ExecContext(ctx.Context,
		`INSERT INTO loans (book_id, member_id, due_at) VALUES (?, ?, ?)`,
		in.BookID, ctx.Deps.MemberID, dueAt.Unix())
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
		// A loan that was created a moment ago cannot be anything else, so this
		// is stated rather than computed. assess exists for rows read back.
		Status: StatusOnLoan,
		DueAt:  dueAt,
	}, nil
}

// listBooks is the read side. It takes no transaction, which is the reason
// Deps.DB is an interface.
//
// Paging is keyset rather than offset: the cursor carries the last id of the
// page it came from, so a book added between two calls cannot shift a row onto
// a page the caller has already seen.
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
	if in.Cursor != "" {
		after, err := decodeCursor(in.Cursor)
		if err != nil {
			return ListOut{}, err
		}
		where = append(where, `id > ?`)
		args = append(args, after)
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
	if err := rows.Err(); err != nil {
		return ListOut{}, err
	}

	// A full page means there may be another. There may equally not be, and the
	// caller finds out by asking -- which is what every keyset pager does,
	// because the alternative is fetching one row more than was asked for on
	// every call to answer a question most callers never ask.
	if in.Limit > 0 && len(out.Books) == in.Limit {
		out.NextCursor = encodeCursor(out.Books[len(out.Books)-1].ID)
	}
	return out, nil
}

// listLoans is the other read side, and the one that is about the caller rather
// than about the catalogue: the member comes from Deps, so this cannot be asked
// for somebody else's history.
func listLoans(ctx services.Ctx[Deps], in ListLoansIn) (ListLoansOut, error) {
	query := `SELECT l.id, l.book_id, b.title, l.due_at, l.returned_at
	          FROM loans l JOIN books b ON b.id = l.book_id
	          WHERE l.member_id = ?`
	if !in.IncludeReturned {
		query += ` AND l.returned_at IS NULL`
	}
	query += ` ORDER BY l.id`

	rows, err := ctx.Deps.DB.QueryContext(ctx.Context, query, ctx.Deps.MemberID)
	if err != nil {
		return ListLoansOut{}, err
	}
	defer func() { _ = rows.Close() }()

	now := ctx.Deps.Now()
	out := ListLoansOut{Loans: []Loan{}}
	for rows.Next() {
		var (
			loan     Loan
			due      int64
			returned sql.NullInt64
		)
		if err := rows.Scan(
			&loan.LoanID, &loan.BookID, &loan.Title, &due, &returned); err != nil {
			return ListLoansOut{}, err
		}
		loan.DueAt = time.Unix(due, 0).UTC()

		var returnedAt *time.Time
		if returned.Valid {
			at := time.Unix(returned.Int64, 0).UTC()
			returnedAt = &at
		}
		loan.Status, loan.FineCents = assess(loan.DueAt, returnedAt, now)
		out.Loans = append(out.Loans, loan)
	}
	return out, rows.Err()
}
