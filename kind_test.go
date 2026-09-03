package services

import "testing"

func TestKindString(t *testing.T) {
	for _, tc := range []struct {
		kind Kind
		want string
	}{
		{Query, "query"},
		{Mutation, "mutation"},
		{Kind(0), "unknown"},
		{Kind(99), "unknown"},
	} {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("Kind(%d) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestKindValid(t *testing.T) {
	// The zero Kind is deliberately invalid: defaulting it would silently pick
	// side-effect semantics on the author's behalf.
	if Kind(0).valid() {
		t.Error("the zero Kind must not be valid")
	}
	if !Query.valid() || !Mutation.valid() {
		t.Error("declared kinds must be valid")
	}
}
