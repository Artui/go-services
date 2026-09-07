package example

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	services "github.com/Artui/go-services"
	"github.com/google/jsonschema-go/jsonschema"
)

// The rendered type, declaring both halves the way a real spec would.
type finedOut struct {
	Fine  Cents   `json:"fine"`
	DueAt Instant `json:"due_at"`
	Loan  int64   `json:"loan_id" jsonschema:"this loan's identifier"`
}

func finedRegistry(t *testing.T) *services.Registry[Deps] {
	t.Helper()
	reg := services.New(resolverOver(audienceDB(t)))
	services.MustRegister(reg, services.Spec[Deps, struct{}, finedOut]{
		Name: "fined", Kind: services.Query,
		Description: "What one loan owes and when it is due.",
		Run: func(services.Ctx[Deps], struct{}) (finedOut, error) {
			return finedOut{
				Fine:  550,
				DueAt: Instant{Time: time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)},
				Loan:  1,
			}, nil
		},
	})
	return reg
}

// decodedResult dispatches and returns the decoded JSON an adapter would hold,
// alongside the output schema the kernel reflected. Together those are exactly
// what an adapter has in hand at the moment it would render.
func decodedResult(t *testing.T, reg *services.Registry[Deps], principal any) (any, *jsonschema.Schema) {
	t.Helper()
	res, err := reg.Dispatch(t.Context(), principal, "fined", nil)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	encoded, err := json.Marshal(res.Value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, entry := range reg.Entries() {
		if entry.Name == "fined" {
			return decoded, entry.Output
		}
	}
	t.Fatal("no entry named fined")
	return nil, nil
}

// The claim the whole mechanism exists for: one declaration, one schema, one
// stored value, and two readers who are told different things -- because what
// differs is the reader and not the field.
func TestOneSchemaRendersDifferentlyForDifferentReaders(t *testing.T) {
	reg := finedRegistry(t)
	renderer := LibraryRenderer()

	want := map[int64]string{
		1: `{"due_at":"Sat 15 Aug 2026 at 9:00pm (NZST)","fine":"NZD 5.50","loan_id":1}`,
		2: `{"due_at":"Sat 15 Aug 2026 at 2:00am (PDT)","fine":"USD 5.50","loan_id":1}`,
	}
	for member, expected := range want {
		decoded, schema := decodedResult(t, reg, member)
		rendered, err := renderer.Render(t.Context(), member, schema, decoded)
		if err != nil {
			t.Fatalf("member %d: %v", member, err)
		}
		if got := encode(t, rendered); got != expected {
			t.Errorf("member %d =\n  %s\nwant\n  %s", member, got, expected)
		}
	}
}

// The half that must not move. An adapter that renders nothing serves exactly
// what it served before, which is what keeps HTTP raw and is the entire reason
// the kernel offers rendering without performing it.
func TestAnUnrenderedDispatchIsUnchanged(t *testing.T) {
	decoded, _ := decodedResult(t, finedRegistry(t), int64(1))

	if got := encode(t, decoded); got != `{"due_at":"2026-08-15T09:00:00Z","fine":550,"loan_id":1}` {
		t.Errorf("unrendered = %s, want the raw values", got)
	}
}

// A field with no keyword is passed through untouched, and so is a whole spec
// that declares none. Rendering is opt-in per field, not a pass over everything.
func TestAFieldWithNoKeywordIsUntouched(t *testing.T) {
	decoded, schema := decodedResult(t, finedRegistry(t), int64(1))

	rendered, err := LibraryRenderer().Render(t.Context(), int64(1), schema, decoded)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := rendered.(map[string]any)["loan_id"]; got != float64(1) {
		t.Errorf("loan_id = %v, want the raw identifier", got)
	}
}

// A keyword no formatter knows leaves the value alone. A schema travels to
// consumers that have never heard of a vocabulary, and dropping a value would
// be a worse answer than showing it raw.
func TestAnUnknownKeywordLeavesTheValueAlone(t *testing.T) {
	empty := services.NewRenderer(map[string]services.Formatter{})
	decoded, schema := decodedResult(t, finedRegistry(t), int64(1))

	rendered, err := empty.Render(t.Context(), int64(1), schema, decoded)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := encode(t, rendered); got != `{"due_at":"2026-08-15T09:00:00Z","fine":550,"loan_id":1}` {
		t.Errorf("rendered = %s, want every value untouched", got)
	}
}

// A formatter that refuses names the field, because the alternative is an error
// saying only that something somewhere could not be rendered.
func TestAFailingFormatterNamesTheField(t *testing.T) {
	broken := services.NewRenderer(map[string]services.Formatter{
		"money-minor": func(context.Context, any, any) (any, error) {
			return nil, errString("no rate for this branch")
		},
	})
	decoded, schema := decodedResult(t, finedRegistry(t), int64(1))

	_, err := broken.Render(t.Context(), int64(1), schema, decoded)

	if err == nil {
		t.Fatal("a failing formatter was not reported")
	}
	for _, want := range []string{"fine", "money-minor", "no rate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
