package adkx_test

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	services "github.com/Artui/go-services"
	"google.golang.org/adk/v2/agent"
)

// deps is the application's own dependency type. Identity lives on it, because
// the registry's resolver is the one place an application says what a principal
// is.
type deps struct {
	user string
}

func resolve(_ context.Context, principal any) (deps, error) {
	user, ok := principal.(string)
	if !ok || user == "" {
		return deps{}, fmt.Errorf("%w: this invocation is not signed in", services.ErrPermission)
	}
	return deps{user: user}, nil
}

type borrowIn struct {
	BookID int64  `json:"book_id" jsonschema:"the book to borrow"`
	Note   string `json:"note,omitempty"`
}

func (in borrowIn) Validate() error {
	if in.BookID <= 0 {
		return services.Invalid("book_id", "must be a positive identifier")
	}
	return nil
}

type borrowOut struct {
	LoanID int64  `json:"loan_id"`
	By     string `json:"by"`
}

type emptyIn struct{}

// operatorText is the sort of thing an unexpected error says. No test may find
// it in anything handed to a model.
const operatorText = "dial tcp 10.0.0.4:5432: connection refused (pool exhausted)"

func newRegistry(t *testing.T) *services.Registry[deps] {
	t.Helper()
	reg := services.New(resolve)

	services.MustRegister(reg, services.Spec[deps, borrowIn, borrowOut]{
		Name:        "borrow_book",
		Description: "Lend one copy of a book to the signed-in member.",
		Kind:        services.Mutation,
		Status:      201,
		Permit: []func(services.Ctx[deps], borrowIn) error{
			func(_ services.Ctx[deps], in borrowIn) error {
				if in.BookID == 13 {
					return fmt.Errorf("%w: book 13 is reference only", services.ErrPermission)
				}
				return nil
			},
		},
		Run: func(c services.Ctx[deps], in borrowIn) (borrowOut, error) {
			if in.BookID == 99 {
				return borrowOut{}, fmt.Errorf("%w: no book %d", services.ErrNotFound, in.BookID)
			}
			if in.BookID == 7 {
				return borrowOut{}, fmt.Errorf("%w: no copy on the shelf", services.ErrConflict)
			}
			return borrowOut{LoanID: in.BookID * 10, By: c.Deps.user}, nil
		},
	})

	// A spec taking no arguments at all, which ADK calls with a nil args.
	services.MustRegister(reg, services.Spec[deps, emptyIn, string]{
		Name: "ping", Kind: services.Query,
		Run: func(services.Ctx[deps], emptyIn) (string, error) { return "pong", nil },
	})

	services.MustRegister(reg, services.Spec[deps, emptyIn, string]{
		Name: "boom", Kind: services.Query,
		Run: func(services.Ctx[deps], emptyIn) (string, error) {
			return "", fmt.Errorf("%s", operatorText)
		},
	})

	// A value the encoder cannot represent: what an average over no rows is.
	services.MustRegister(reg, services.Spec[deps, emptyIn, float64]{
		Name: "average", Kind: services.Query,
		Run: func(services.Ctx[deps], emptyIn) (float64, error) { return math.NaN(), nil },
	})

	// Returns a typed nil, which errors.As matches. Before the kernel and the
	// MCP adapter were both hardened this shape ended a process.
	services.MustRegister(reg, services.Spec[deps, emptyIn, string]{
		Name: "typed_nil", Kind: services.Query,
		Run: func(services.Ctx[deps], emptyIn) (string, error) {
			var invalid *services.ValidationError
			return "", invalid
		},
	})

	return reg
}

// toolContext is an ADK invocation context for a signed-in user.
//
// StrictContextMock is ADK's own test double and implements the whole surface,
// so this keeps compiling as those interfaces grow -- which matters more here
// than in most fakes, because agent.Context is large and none of it is ours.
type toolContext struct {
	agent.StrictContextMock
	user string
}

func (c *toolContext) UserID() string { return c.user }

func contextFor(t *testing.T, user string) agent.Context {
	t.Helper()
	return &toolContext{StrictContextMock: agent.NewStrictContextMock(t.Context()), user: user}
}

// contains is a readability helper for the assertions below.
func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
