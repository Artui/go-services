package services

import (
	"errors"
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
