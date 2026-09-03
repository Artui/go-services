package services

import (
	"encoding/json"
	"strconv"

	"github.com/google/jsonschema-go/jsonschema"
)

// EncodeParams folds an HTTP request's parameters into a JSON payload,
// coercing each one to the type the schema declares for it.
//
// It lives in the kernel rather than in an adapter because every HTTP-shaped
// transport needs exactly this and needs it to agree: a query string is all
// strings, so "?limit=10" has to become {"limit": 10} and not {"limit": "10"},
// and the schema is the only thing that knows which.
//
// Precedence runs body, then query, then captures, so a route capture always
// wins. That ordering is a security rule, not a convenience: an operation
// mounted at /tenants/{tenant}/reports is scoped by its path, and an adapter
// that merged the other way would let a client rescope it with ?tenant=other.
//
// The kernel therefore takes the two sources separately and does the merge
// itself. An earlier version took one merged map and stated the ordering in
// this comment, which is a rule an adapter can get backwards while every test
// still passes and the scope silently leaks.
//
// A key with no matching property is dropped rather than rejected: a query
// string carries analytics noise that is none of the operation's business.
// Unknown keys inside body are still rejected, by the schema itself.
func EncodeParams(
	s *jsonschema.Schema,
	body json.RawMessage,
	query, captures map[string][]string,
) (json.RawMessage, error) {
	if len(query) == 0 && len(captures) == 0 {
		return body, nil
	}

	payload := map[string]any{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, malformedBody(err)
		}
	}

	// Applied lowest precedence first, so a later source overwrites an earlier
	// one on the same key.
	for _, source := range []map[string][]string{query, captures} {
		if err := overlay(s, payload, source); err != nil {
			return nil, err
		}
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, &ValidationError{
			Fields: map[string][]string{NonFieldKey: {"unencodable parameters: " + err.Error()}},
		}
	}
	return raw, nil
}

// overlay coerces one source's values onto payload.
func overlay(s *jsonschema.Schema, payload map[string]any, source map[string][]string) error {
	for key, values := range source {
		if len(values) == 0 {
			continue
		}
		prop := property(s, key)
		if prop == nil {
			continue
		}
		coerced, err := coerce(prop, values)
		if err != nil {
			return Invalid(key, err.Error())
		}
		payload[key] = coerced
	}
	return nil
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
