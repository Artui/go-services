package httpx

import (
	"net/http"
	"strings"
)

// wildcards returns the names a ServeMux pattern captures, in the order they
// appear.
//
// net/http will say which pattern a request matched and what one named
// wildcard captured, but there is no way to ask it for the names -- so the
// adapter reads them back off the pattern string, which is the same string
// ServeMux itself parsed.
//
// The syntax is "[METHOD ][HOST]/[PATH]" with {name} and {name...} segments.
// {$} is not a capture: it anchors the pattern to the end of the path.
func wildcards(pattern string) []string {
	var names []string
	for {
		start := strings.IndexByte(pattern, '{')
		if start < 0 {
			return names
		}
		pattern = pattern[start+1:]

		end := strings.IndexByte(pattern, '}')
		if end < 0 {
			// An unterminated brace, which no caller can now supply: Mount
			// reads capture names only after ServeMux has accepted the
			// pattern, and Request.Pattern is a pattern ServeMux accepted. The
			// guard stays because it is the bound on the slice below, not
			// because a path reaches it -- TestWildcards is what exercises it.
			return names
		}
		name := pattern[:end]
		pattern = pattern[end+1:]

		if name == "$" {
			continue
		}
		// A multi-segment wildcard is declared "{rest...}" and read back as
		// "rest".
		names = append(names, strings.TrimSuffix(name, "..."))
	}
}

// patternValues reads a request's path captures off the pattern it matched.
//
// Request.Pattern is empty unless a ServeMux matched the request, so a handler
// served directly, or wrapped by a router with its own capture syntax, simply
// gets no captures rather than wrong ones. WithPathValues is how such a router
// supplies them.
//
// Reading the names per request rather than at construction is also what lets
// one handler answer on several patterns: mount the same Handler under
// "/authors/{id}" and "/v1/authors/{id}" and each request binds whatever its
// own pattern captured.
//
// The result is shaped as url.Values because that is what the kernel's
// EncodeParams takes: one merged map of flat string parameters, whatever wire
// position each one arrived in.
func patternValues(r *http.Request) map[string][]string {
	names := wildcards(r.Pattern)
	if len(names) == 0 {
		return nil
	}
	values := make(map[string][]string, len(names))
	for _, name := range names {
		values[name] = []string{r.PathValue(name)}
	}
	return values
}
