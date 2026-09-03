package services

import (
	"bytes"
	"encoding/json"
	"errors"
)

var errTrailingData = errors.New("unexpected data after the top-level JSON value")

// malformedBody builds the error for a payload that is not JSON at all.
//
// One helper rather than a string at each site: the kernel rejects a malformed
// body from two places, and which one fires depends on whether the client
// happened to append a query parameter. Two wordings for one condition, chosen
// by something the client cannot see, is not a distinction worth shipping.
func malformedBody(err error) *ValidationError {
	return &ValidationError{
		Fields: map[string][]string{NonFieldKey: {"malformed JSON body: " + err.Error()}},
	}
}

// decodeJSONValue parses exactly one JSON value from raw.
//
// Both callers go through here so the two cannot report the same malformed body
// with different words. That had already happened once and been fixed by making
// the wording match; it happened again the moment one side switched to a
// json.Decoder, whose "unexpected EOF" is not Unmarshal's "unexpected end of
// JSON input". A shared parse makes the property structural rather than
// something a test has to keep policing.
//
// exact preserves number literals as json.Number instead of collapsing them
// into float64. A caller that will re-encode the value needs it, or the
// re-encoded body is not the one the client sent. A caller that only inspects
// the shape does not, and float64 is what the schema validator expects.
func decodeJSONValue(raw []byte, exact bool) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if exact {
		decoder.UseNumber()
	}
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	// Decode stops at the end of the first value, where Unmarshal rejected
	// anything after it. Keep that strictness rather than inheriting the
	// decoder's laxer contract.
	if decoder.More() {
		return nil, errTrailingData
	}
	return value, nil
}
