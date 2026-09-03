package services

import (
	"errors"
	"fmt"
	"testing"
)

func TestValidationErrorMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *ValidationError
		want string
	}{
		{"empty", &ValidationError{}, "services: validation failed"},
		{"one field", Invalid("name", "must not be blank"),
			"services: validation failed: name: must not be blank"},
		{"joins messages", Invalid("name", "too short", "too rude"),
			"services: validation failed: name: too short; too rude"},
		{"sorted for stability", &ValidationError{Fields: map[string][]string{
			"zeta": {"z"}, "alpha": {"a"},
		}}, "services: validation failed: alpha: a, zeta: z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSentinelsAreDistinct(t *testing.T) {
	wrapped := errors.Join(ErrNotFound)
	if !errors.Is(wrapped, ErrNotFound) {
		t.Error("wrapping should preserve ErrNotFound")
	}
	if errors.Is(ErrNotFound, ErrConflict) || errors.Is(ErrConflict, ErrPermission) {
		t.Error("sentinels must not compare equal")
	}
}

func TestValidationErrorFieldMapIsNeverNil(t *testing.T) {
	// A &ValidationError{} with no map reaches a renderer sooner or later, and
	// rendering Fields directly would put {"errors": null} on the wire.
	if got := (&ValidationError{}).FieldMap(); got == nil || len(got) != 0 {
		t.Errorf("FieldMap = %#v, want an empty non-nil map", got)
	}
	populated := Invalid("name", "must not be blank")
	if got := populated.FieldMap(); len(got["name"]) != 1 {
		t.Errorf("FieldMap = %#v, want the declared messages", got)
	}
}

func TestStatusFor(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"validation", Invalid("name", "bad"), 400},
		{"wrapped validation", fmt.Errorf("layer: %w", Invalid("n", "bad")), 400},
		{"permission", ErrPermission, 403},
		{"not found", fmt.Errorf("looking up: %w", ErrNotFound), 404},
		{"conflict", ErrConflict, 409},
		{"too large", ErrBodyTooLarge, StatusBodyTooLarge},
		// The safe direction: an unrecognised error is a bug until proven
		// otherwise, and 400 would blame a caller whose request was fine.
		{"anything else", errors.New("boom"), 500},
		{"nil", nil, 500},
	} {
		if got := StatusFor(tc.err); got != tc.want {
			t.Errorf("%s: StatusFor = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestConfigurationFaultIsNotAClientError(t *testing.T) {
	// The distinction the taxonomy exists to make: a validation failure is
	// addressed to the caller, a configuration fault to whoever deployed the
	// route, and only one of them is something the caller can act on.
	cfg := fmt.Errorf("%w: bad route", ErrConfiguration)
	if StatusFor(cfg) != 500 {
		t.Errorf("StatusFor = %d, want 500", StatusFor(cfg))
	}
	var invalid *ValidationError
	if errors.As(cfg, &invalid) {
		t.Error("a configuration fault must not read as a validation failure")
	}
}

// A helper typed func(...) *ValidationError, returned straight into an error,
// produces a non-nil error holding a nil pointer -- and errors.As matches it.
// Every adapter's "is this a validation failure" arm then hands a nil receiver
// to a renderer. On a transport whose server recovers that is a 500; on one
// whose handler runs on a goroutine nobody recovers, it ends the process.
func TestValidationErrorToleratesANilReceiver(t *testing.T) {
	var typed *ValidationError
	var err error = typed

	var target *ValidationError
	if !errors.As(err, &target) {
		t.Fatal("errors.As is expected to match a typed nil; the test premise is wrong")
	}
	if target != nil {
		t.Fatal("target should be the nil pointer")
	}

	// Neither may panic.
	if got := target.FieldMap(); got == nil || len(got) != 0 {
		t.Errorf("FieldMap on a nil receiver = %#v, want an empty non-nil map", got)
	}
	if got := target.Error(); got == "" {
		t.Error("Error on a nil receiver must still describe itself")
	}
}

func TestValidSuccessStatus(t *testing.T) {
	for status, want := range map[int]bool{
		0: false, 99: false, 100: false, 103: false, 199: false,
		200: true, 204: true, 404: true, 599: true, 600: false, -1: false,
	} {
		if got := ValidSuccessStatus(status); got != want {
			t.Errorf("ValidSuccessStatus(%d) = %v, want %v", status, got, want)
		}
	}
}
