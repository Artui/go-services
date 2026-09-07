package example

// The declaration half, and the two formatters this library's own domain needs.
//
// Both are deliberately the cases the earlier finding named as the ones a
// description could not reach: money that states a unit but not a currency, and
// a timestamp that states a zone but not the reader's.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

// Cents is money in minor units. The type carries the declaration so that every
// field of this type gets it, which is the answer to writing the same sentence
// on each one by hand.
type Cents int64

// JSONSchema declares both halves: the sentence a model reads whether or not
// anything renders, and the keyword a renderer acts on when something does.
//
// The sentence stays even though the keyword is present, because the two serve
// different readers. A consumer that renders nothing -- an HTTP client, or an
// adapter that never calls a Renderer -- still gets the raw integer, and the
// only thing standing between it and a wrong answer is the description.
func (Cents) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{
		Type:        "integer",
		Description: "an amount of money in minor units, so 550 is 5.50 of whatever currency the branch keeps",
		Extra:       map[string]any{RenderKeyword: "money-minor"},
	}, nil
}

// Instant is a moment in time, always serialised in UTC.
//
// Embedded rather than defined over time.Time, which is the trap the kernel now
// refuses at registration: a defined type does not inherit MarshalJSON, and
// time.Time's fields are unexported, so `type Instant time.Time` would advertise
// a string and serve `{}`.
type Instant struct{ time.Time }

// JSONSchema declares the same two halves Cents does: the sentence for a reader
// that renders nothing, and the keyword for one that does.
func (Instant) JSONSchema() (*jsonschema.Schema, error) {
	return &jsonschema.Schema{
		Type:        "string",
		Format:      "date-time",
		Description: "a moment in time as an RFC 3339 timestamp in UTC",
		Extra:       map[string]any{RenderKeyword: "instant"},
	}, nil
}

// Reader is what a formatter needs to know about whoever is asking. An adapter
// resolves it from the principal, outside any transaction, because presentation
// is a read and does not belong inside the boundary a write is protected by.
type Reader struct {
	Location *time.Location
	Currency string
}

// ReadersOf resolves the reader for a principal. In a real application this is a
// lookup; here it is a table, because what is being demonstrated is that the
// answer varies by reader and not how a database stores it.
func ReadersOf(principal any) Reader {
	member, _ := principal.(int64)
	switch member {
	case 1:
		return Reader{Location: time.FixedZone("NZST", 12*3600), Currency: "NZD"}
	case 2:
		return Reader{Location: time.FixedZone("PDT", -7*3600), Currency: "USD"}
	default:
		return Reader{Location: time.UTC, Currency: "USD"}
	}
}

// MoneyMinor renders minor units as an amount with its currency.
//
// The currency comes from the reader and not from the declaration, which is the
// whole point: one schema serves a branch that keeps dollars and a branch that
// keeps euros, and the field cannot name either without being wrong for the
// other.
func MoneyMinor(_ context.Context, principal any, value any) (any, error) {
	minor, ok := value.(float64)
	if !ok {
		// A JSON number decodes to float64. Anything else under this keyword is
		// a declaration that does not match its own field, and rendering it
		// would invent an amount.
		return nil, fmt.Errorf("money-minor wants a number, got %T", value)
	}
	reader := ReadersOf(principal)
	whole := int64(minor) / 100
	part := int64(minor) % 100
	if part < 0 {
		part = -part
	}
	return fmt.Sprintf("%s %d.%02d", reader.Currency, whole, part), nil
}

// RenderInstant states a UTC timestamp in the reader's own zone.
//
// This is the case no description can reach. "in UTC" is true and useless: the
// reader's zone is not in the payload, is not in the schema, and cannot be,
// because it is not a fact about the field.
func RenderInstant(_ context.Context, principal any, value any) (any, error) {
	text, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("instant wants a string, got %T", value)
	}
	at, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return nil, fmt.Errorf("instant %q is not RFC 3339: %w", text, err)
	}
	reader := ReadersOf(principal)
	return at.In(reader.Location).Format("Mon 2 Jan 2006 at 3:04pm (MST)"), nil
}

// LibraryRenderer is the set this module's own specs need.
func LibraryRenderer() *Renderer {
	return NewRenderer(map[string]Formatter{
		"money-minor": MoneyMinor,
		"instant":     RenderInstant,
	})
}
