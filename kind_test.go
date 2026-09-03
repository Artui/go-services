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

func TestKindAllowsMethod(t *testing.T) {
	for _, tc := range []struct {
		kind   Kind
		method string
		ok     bool
	}{
		{Query, "GET", true},
		{Query, "HEAD", true},
		{Query, "OPTIONS", true},
		{Query, "POST", false},
		{Query, "PUT", false},
		{Query, "PATCH", false},
		// The gap that made this a kernel concern: two adapters agreed on a
		// rule of two prohibitions, and both then accepted a query on DELETE.
		{Query, "DELETE", false},
		{Mutation, "POST", true},
		{Mutation, "PUT", true},
		{Mutation, "PATCH", true},
		{Mutation, "DELETE", true},
		{Mutation, "GET", false},
		// The other half of the same gap.
		{Mutation, "HEAD", false},
		{Mutation, "OPTIONS", false},
		// A route table written by hand says "post" as often as "POST".
		{Mutation, "post", true},
		{Query, "  get  ", true},
		{Kind(0), "GET", false},
	} {
		err := tc.kind.AllowsMethod(tc.method)
		if (err == nil) != tc.ok {
			t.Errorf("%s on %q: got %v, want ok=%v", tc.kind, tc.method, err, tc.ok)
		}
	}
}
