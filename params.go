package services

import (
	"encoding/json"
	"strconv"

	"github.com/google/jsonschema-go/jsonschema"
)

// EncodeParams folds flat string parameters into a JSON payload, coercing each
// one to the type the schema declares for it.
//
// It lives in the kernel rather than in an adapter because every HTTP-shaped
// transport needs exactly this and needs it to agree: a query string is all
// strings, so "?limit=10" has to become {"limit": 10} and not {"limit": "10"},
// and the schema is the only thing that knows which. Having two HTTP adapters
// is what proved the point -- the second one would otherwise have re-guessed it.
//
// params overwrite body on a key collision. An adapter that carries both route
// captures and query parameters merges them first, with captures winning, so a
// filter value cannot override a route scope.
//
// A key with no matching property is dropped rather than rejected: a query
// string carries analytics noise that is none of the operation's business.
// Unknown keys inside body are still rejected, by the schema itself.
func EncodeParams(
	s *jsonschema.Schema, params map[string][]string, body json.RawMessage,
) (json.RawMessage, error) {
	if len(params) == 0 {
		return body, nil
	}

	payload := map[string]any{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, &ValidationError{
				Fields: map[string][]string{NonFieldKey: {"malformed JSON body: " + err.Error()}},
			}
		}
	}

	for key, values := range params {
		if len(values) == 0 {
			continue
		}
		prop := property(s, key)
		if prop == nil {
			continue
		}
		coerced, err := coerce(prop, values)
		if err != nil {
			return nil, Invalid(key, err.Error())
		}
		payload[key] = coerced
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, &ValidationError{
			Fields: map[string][]string{NonFieldKey: {"unencodable parameters: " + err.Error()}},
		}
	}
	return raw, nil
}

func property(s *jsonschema.Schema, key string) *jsonschema.Schema {
	if s == nil || s.Properties == nil {
		return nil
	}
	return s.Properties[key]
}

// coerce turns the raw string values of one parameter into the JSON value its
// schema calls for.
func coerce(prop *jsonschema.Schema, values []string) (any, error) {
	kind := schemaType(prop)
	if kind == "array" {
		items := make([]any, 0, len(values))
		for _, v := range values {
			item, err := scalar(schemaType(prop.Items), v)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	}
	// Repeated keys on a scalar field take the first, matching url.Values.Get.
	return scalar(kind, values[0])
}

func scalar(kind, v string) (any, error) {
	switch kind {
	case "integer":
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, errNotA("an integer", v)
		}
		return n, nil
	case "number":
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, errNotA("a number", v)
		}
		return f, nil
	case "boolean":
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, errNotA("a boolean", v)
		}
		return b, nil
	default:
		// Strings, and anything the schema did not pin down, pass through for
		// the schema itself to judge.
		return v, nil
	}
}

// schemaType reports the single type a schema declares, ignoring a null
// alternative so that a nullable field still coerces as its real type.
func schemaType(s *jsonschema.Schema) string {
	if s == nil {
		return ""
	}
	if s.Type != "" {
		return s.Type
	}
	for _, t := range s.Types {
		if t != "null" {
			return t
		}
	}
	return ""
}

type coercionError struct{ want, got string }

func (e *coercionError) Error() string { return "expected " + e.want + ", got " + strconv.Quote(e.got) }

func errNotA(want, got string) error { return &coercionError{want: want, got: got} }
