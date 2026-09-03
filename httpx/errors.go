package httpx

import (
	"errors"
	"net/http"

	services "github.com/Artui/go-services"
)

// errorResponse is the body for every failure that is not a validation
// failure. One field, so a client can show something without first working out
// which of 403, 404, 409 and 413 it was handed.
type errorResponse struct {
	Error string `json:"error"`
}

// validationResponse is the body for a 400. It mirrors ValidationError.FieldMap
// exactly, because the kernel already keys that map by the JSON name the client
// sent rather than by the Go field name.
type validationResponse struct {
	Errors map[string][]string `json:"errors"`
}

// internalBody is the 500 body, pre-encoded.
//
// It is a literal rather than a marshal call because it is the fallback used
// when marshalling has already failed once: a fallback that can fail the same
// way as the thing it stands in for is not a fallback. A test asserts it is
// byte-for-byte what the encoder would have produced from the kernel's own
// constant.
var internalBody = []byte(`{"error":"` + services.InternalErrorText + `"}`)

// errorResponseFor maps a kernel error onto the status and the body a client
// gets for it.
//
// Neither the statuses nor the sentences are this package's. They were, for one
// round: two HTTP adapters were each given the table as prose and each wrote
// its own copy, which is the same mistake the kernel had already corrected for
// parameter coercion. They live in services now, in a package that imports no
// transport, so a third adapter cannot answer 200 where this one answers 409 or
// put an operator's error text on the wire.
//
// What is left here is the shape of the body, which is a wire format and so
// genuinely an adapter's business.
func errorResponseFor(err error) (int, any) {
	status := services.StatusFor(err)

	// The 400 body is the only one built from the error's own structure rather
	// than from a fixed sentence, so it is the only case that re-examines err.
	// FieldMap rather than Fields: the map is exported on a constructible
	// struct, and {"errors": null} is not something a client parsing an object
	// can read.
	var invalid *services.ValidationError
	if errors.As(err, &invalid) {
		return status, validationResponse{Errors: invalid.FieldMap()}
	}

	switch status {
	case services.StatusBodyTooLarge:
		// The wrapped error names the limit, which is for the observer. The
		// client is told only that it sent too much.
		return status, errorResponse{Error: services.BodyTooLargeText}
	case http.StatusInternalServerError:
		// An unexpected error's words are written for an operator: they name
		// tables, hosts, query fragments and internal identifiers. Returning
		// them is how that detail reaches a stranger. The real error goes to
		// the WithOnError observer instead, which is the half that makes this
		// redaction affordable -- redacting with nowhere to send it would mean
		// nobody ever sees the failure at all.
		return status, errorResponse{Error: services.InternalErrorText}
	default:
		return status, errorResponse{Error: err.Error()}
	}
}

// bodyAllowedForStatus reports whether a response with this status may carry a
// body.
//
// A spec declaring Status 204 for a delete is the common case, and net/http
// discards a body written under one silently -- so a handler that marshals the
// value anyway passes every test that only reads the status code, and puts a
// Content-Type header on a response that has no content.
func bodyAllowedForStatus(status int) bool {
	return status != http.StatusNoContent && status != http.StatusNotModified
}
