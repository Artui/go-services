package services

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// A Location template is a path with {name} placeholders, each naming a field
// of the operation's output by its JSON name:
//
//	"/loans/{loan_id}"
//
// It lives here for the reason StatusFor and EncodeParams do: both HTTP
// adapters need it, the result is client-visible, and two adapters carrying
// their own copies is a difference a caller can observe -- one that would show
// up as two servers disagreeing about where the thing they just created lives.
//
// The syntax is {name} on both adapters even though Gin writes :name in its
// route patterns. A Location is not a pattern being matched, it is a string
// being filled, and borrowing a router's matching syntax for it would suggest
// the two are related.

// ExpandLocation fills a Location template from an operation's output.
//
// Values are taken by JSON name, so what fills {loan_id} is whatever the output
// marshals under "loan_id" -- the same names the schema advertises and the same
// ones a validation error is keyed by. A field the template names but the value
// does not carry is ErrConfiguration: the route is wrong, and no request could
// have made it right.
//
// Each value is path-escaped. That is not tidiness: a title or a slug reaching
// a Location unescaped can contain a slash, and a header saying the created
// thing lives at a path the server never meant is worth more to an attacker
// than to a client.
func ExpandLocation(template string, value any) (string, error) {
	names, err := locationPlaceholders(template)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return template, nil
	}

	fields, err := locationFields(value)
	if err != nil {
		return "", err
	}

	out := template
	for _, name := range names {
		field, ok := fields[name]
		if !ok {
			return "", fmt.Errorf(
				"%w: the Location template names %q, which the output does not carry",
				ErrConfiguration, name)
		}
		text, err := locationValue(name, field)
		if err != nil {
			return "", err
		}
		out = strings.ReplaceAll(out, "{"+name+"}", url.PathEscape(text))
	}
	return out, nil
}

// locationPlaceholders returns the names a template asks for, in order.
//
// An unbalanced brace is refused rather than treated as a literal. A template
// reading "/loans/{loan_id" is a typo every time, and serving it verbatim would
// put a brace in a URL and leave the mistake to be found by whoever followed
// the header.
func locationPlaceholders(template string) ([]string, error) {
	var names []string
	rest := template
	for {
		open := strings.IndexByte(rest, '{')
		if open < 0 {
			if strings.IndexByte(rest, '}') >= 0 {
				return nil, fmt.Errorf(
					"%w: the Location template %q closes a placeholder it never opened",
					ErrConfiguration, template)
			}
			return names, nil
		}
		rest = rest[open+1:]
		close := strings.IndexByte(rest, '}')
		if close < 0 {
			return nil, fmt.Errorf(
				"%w: the Location template %q opens a placeholder it never closes",
				ErrConfiguration, template)
		}
		name := rest[:close]
		if name == "" || strings.IndexByte(name, '{') >= 0 {
			return nil, fmt.Errorf(
				"%w: the Location template %q has an empty or nested placeholder",
				ErrConfiguration, template)
		}
		names = append(names, name)
		rest = rest[close+1:]
	}
}

// locationFields renders the output as its JSON names.
//
// Going through the encoder rather than reflecting over the struct is what
// makes the names the same ones the client sees: json tags, embedded structs
// and a custom MarshalJSON are all honoured, because this is the same encoder
// that produces the body.
//
// UseNumber is load-bearing and has cost this project once already. Decoding
// into an any turns every JSON number into a float64, so an identifier past
// 2^53 would be rewritten on its way into the header -- a Location pointing at
// a row one off from the one just created, with no error anywhere.
func locationFields(value any) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf(
			"%w: the output cannot be encoded, so a Location cannot be built from it: %w",
			ErrConfiguration, err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()

	var fields map[string]any
	if err := decoder.Decode(&fields); err != nil {
		return nil, fmt.Errorf(
			"%w: a Location can only be built from an object output, not %s",
			ErrConfiguration, strings.TrimSpace(string(raw)))
	}
	return fields, nil
}

// locationValue renders one field as a path segment.
//
// A composite is refused rather than rendered. There is no reading of an object
// or an array that belongs in a URL path, and picking one -- its first element,
// its JSON -- would be inventing a convention nobody asked for.
func locationValue(name string, field any) (string, error) {
	switch v := field.(type) {
	case string:
		return v, nil
	case json.Number:
		return v.String(), nil
	case bool:
		return strconv.FormatBool(v), nil
	}

	// Whatever is left came from a JSON decode with UseNumber, so it is one of
	// exactly three things. There is no default arm here on purpose: a fourth
	// case cannot arrive, and writing an arm for one would be an unreachable
	// statement claiming otherwise.
	kind := "null"
	switch field.(type) {
	case map[string]any:
		kind = "an object"
	case []any:
		kind = "an array"
	}
	return "", fmt.Errorf(
		"%w: the Location template names %q, which is %s rather than a value a path can carry",
		ErrConfiguration, name, kind)
}
