package httpx

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	services "github.com/Artui/go-services"
)

// wildcards is exercised through the public API everywhere else. It is tested
// directly here for the inputs no caller can now deliver: Mount reads capture
// names only after ServeMux has accepted the pattern, and Request.Pattern is
// one ServeMux accepted, so a malformed brace reaches this function from
// nowhere but this test. The guard it exercises is the bound on a slice, and
// this is the only thing holding it.
func TestWildcards(t *testing.T) {
	cases := map[string]struct {
		pattern string
		want    string
	}{
		"no captures":            {pattern: "/authors", want: ""},
		"one":                    {pattern: "/authors/{id}", want: "id"},
		"several, in order":      {pattern: "/a/{x}/b/{y}", want: "x,y"},
		"the end-of-path anchor": {pattern: "/authors/{$}", want: ""},
		"a multi-segment name":   {pattern: "/files/{rest...}", want: "rest"},
		"a host in the pattern":  {pattern: "example.com/authors/{id}", want: "id"},
		"an unterminated brace":  {pattern: "/authors/{id", want: ""},
	}

	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			if got := strings.Join(wildcards(tc.pattern), ","); got != tc.want {
				t.Errorf("wildcards(%q) = %q, want %q", tc.pattern, got, tc.want)
			}
		})
	}
}

// internalBody is written as a literal so that the fallback for a failed
// marshal cannot itself fail to marshal. This is the check that keeps the
// literal honest: it must be exactly what the encoder would have produced from
// the kernel's own sentence.
func TestInternalBodyMatchesItsEncoder(t *testing.T) {
	want, err := json.Marshal(errorResponse{Error: services.InternalErrorText})
	if err != nil {
		t.Fatalf("marshalling the 500 body: %v", err)
	}
	if string(internalBody) != string(want) {
		t.Errorf("internalBody = %s, want %s", internalBody, want)
	}
}

func TestBodyAllowedForStatus(t *testing.T) {
	cases := map[int]bool{
		http.StatusOK:          true,
		http.StatusCreated:     true,
		http.StatusNoContent:   false,
		http.StatusNotModified: false,
		http.StatusBadRequest:  true,
	}
	for status, want := range cases {
		if got := bodyAllowedForStatus(status); got != want {
			t.Errorf("bodyAllowedForStatus(%d) = %v, want %v", status, got, want)
		}
	}
}
