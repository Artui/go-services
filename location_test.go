package services

import (
	"errors"
	"math"
	"strings"
	"testing"
)

type loanOut struct {
	LoanID   int64  `json:"loan_id"`
	Slug     string `json:"slug"`
	Returned bool   `json:"returned"`
}

func TestExpandLocation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
		value    any
		want     string
	}{
		{
			name:     "a placeholder is filled from the output's JSON name",
			template: "/loans/{loan_id}", value: loanOut{LoanID: 7},
			want: "/loans/7",
		},
		{
			name:     "several placeholders, and one used twice",
			template: "/loans/{loan_id}/{slug}/{loan_id}",
			value:    loanOut{LoanID: 7, Slug: "brooks"},
			want:     "/loans/7/brooks/7",
		},
		{
			name:     "no placeholders is a fixed path",
			template: "/loans", value: loanOut{LoanID: 7},
			want: "/loans",
		},
		{
			name:     "a bool renders as a bool",
			template: "/loans/{returned}", value: loanOut{Returned: true},
			want: "/loans/true",
		},
		{
			// The float64 round trip that has already cost this project once.
			// Decoded into an any without UseNumber this is 9007199254740992,
			// and the header would name the row next to the one just created.
			name:     "an identifier past 2^53 is not rewritten",
			template: "/loans/{loan_id}", value: loanOut{LoanID: 9007199254740993},
			want: "/loans/9007199254740993",
		},
		{
			// A slug carrying a slash would otherwise forge a path segment.
			name:     "a value is path-escaped",
			template: "/loans/{slug}", value: loanOut{Slug: "a/b c"},
			want: "/loans/a%2Fb%20c",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandLocation(tc.template, tc.value)
			if err != nil {
				t.Fatalf("ExpandLocation: %v", err)
			}
			if got != tc.want {
				t.Errorf("= %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExpandLocationRefuses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
		value    any
		says     string
	}{
		{
			name:     "a placeholder the output does not carry",
			template: "/loans/{nope}", value: loanOut{}, says: "does not carry",
		},
		{
			name: "an unclosed placeholder", template: "/loans/{loan_id",
			value: loanOut{}, says: "never closes",
		},
		{
			name: "a closing brace with no opening one", template: "/loans/loan_id}",
			value: loanOut{}, says: "never opened",
		},
		{
			name: "an empty placeholder", template: "/loans/{}",
			value: loanOut{}, says: "empty or nested",
		},
		{
			name: "a nested placeholder", template: "/loans/{a{b}}",
			value: loanOut{}, says: "empty or nested",
		},
		{
			name: "an output that cannot be encoded", template: "/loans/{loan_id}",
			value: map[string]any{"loan_id": math.NaN()}, says: "cannot be encoded",
		},
		{
			name: "an output that is not an object", template: "/loans/{loan_id}",
			value: []int{1, 2}, says: "only be built from an object",
		},
		{
			name: "a field that is null", template: "/loans/{loan_id}",
			value: map[string]any{"loan_id": nil}, says: "which is null",
		},
		{
			name: "a field that is an object", template: "/loans/{loan_id}",
			value: map[string]any{"loan_id": map[string]int{"a": 1}}, says: "which is an object",
		},
		{
			name: "a field that is an array", template: "/loans/{loan_id}",
			value: map[string]any{"loan_id": []int{1}}, says: "which is an array",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandLocation(tc.template, tc.value)
			if err == nil {
				t.Fatalf("ExpandLocation = %q, want an error", got)
			}
			// Configuration, not validation: the route is wrong, and answering
			// this as a 400 would tell a caller to fix a request that was never
			// the problem.
			if !errors.Is(err, ErrConfiguration) {
				t.Errorf("err = %v, want ErrConfiguration", err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("err = %v, want it to mention %q", err, tc.says)
			}
		})
	}
}
