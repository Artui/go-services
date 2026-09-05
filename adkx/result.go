package adkx

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	services "github.com/Artui/go-services"
)

// InternalErrorText is what a tool result says when the call failed for a
// reason outside the kernel's error taxonomy.
//
// ADK renders a returned error as map[string]any{"error": err.Error()} and puts
// that in front of the model, so an unexpected error's words -- which name
// hosts, tables and internal state -- would be read by something that will
// repeat them to a user or try to act on them. The wire gets a fixed sentence
// and the real error goes to the toolset's ErrorReporter.
//
// It is exported so a consumer can assert on it without matching prose. The
// wording is deliberately close to mcpx's: both are addressed to a model
// deciding what to do next, and both say outright that the arguments were not
// the problem, because a model told only "it failed" will retry a call that
// cannot succeed.
const InternalErrorText = "The service failed unexpectedly. " +
	"The reason was recorded on the server and is not available here. " +
	"The arguments were accepted, so changing them is unlikely to help."

// resultKey is where a non-object output is put.
//
// ADK requires a tool result to be a map, and adk-go's own functiontool wraps a
// scalar under "result" for exactly this reason. Matching it means a spec
// returning a bare string reads the same through this package as through a
// hand-written ADK tool.
const resultKey = "result"

// refuse decides what a failed dispatch tells the model, and whether the real
// error still needs an operator.
//
// The taxonomy's three client-facing members are returned as they are: their
// words were chosen by a spec author who knew a caller would read them, and
// since kernel v0.4.0 they no longer carry a package prefix, so what the model
// sees is a sentence about the domain. A validation failure is rendered
// per-field, because the model's next move is to correct an argument.
//
// The second return says whether the taxonomy recognised the error, so the
// caller knows whether to involve its reporter.
func refuse(err error) (error, bool) {
	var invalid *services.ValidationError
	switch {
	// The nil check is not redundant with errors.As. A helper returning
	// *services.ValidationError assigned into an error yields a non-nil error
	// holding a nil pointer, and errors.As matches it -- which would render as
	// "the arguments were rejected" with nothing listed, inviting a retry of a
	// call that cannot succeed. Classified as unexpected it reaches the
	// reporter, which is where a bug of that shape belongs.
	case errors.As(err, &invalid) && invalid != nil:
		return errors.New(explainValidation(invalid)), true

	case errors.Is(err, services.ErrPermission),
		errors.Is(err, services.ErrNotFound),
		errors.Is(err, services.ErrConflict):
		return err, true
	}
	// The text is three sentences addressed to a model, not a Go error string
	// that will be wrapped and concatenated into a longer one -- ADK renders it
	// as map[string]any{"error": ...} and shows it as it is. Trimming the final
	// stop to satisfy the convention would leave a paragraph ending mid-thought
	// in front of the one reader it has.
	//nolint:staticcheck // ST1005: this string is prose for an LLM, not a Go error message
	return errors.New(InternalErrorText), false
}

// explainValidation renders per-field messages as something a model can act on.
//
// ValidationError.Error joins everything onto one line, which is right for a Go
// log and wrong here: the reader is deciding which argument to change, so the
// fields go one per line and the text says outright that retrying is expected.
//
// The kernel's non-field key is rendered without its key. It names the payload
// as a whole rather than an argument, and printing "non_field_errors" beside
// real argument names invites a model to go looking for an argument by that
// name.
func explainValidation(e *services.ValidationError) string {
	var b strings.Builder
	b.WriteString("The arguments were rejected. Correct these and call the tool again:")
	for _, name := range sortedFields(e) {
		for _, message := range e.FieldMap()[name] {
			b.WriteString("\n- ")
			if name != services.NonFieldKey {
				b.WriteString(name)
				b.WriteString(": ")
			}
			b.WriteString(message)
		}
	}
	return b.String()
}

// succeed renders a dispatch that ran as the map ADK requires.
//
// The value goes through the JSON encoder rather than reflection, so the keys
// are the ones the schema advertises and the ones every other transport uses.
// UseNumber on the way back is what keeps a large identifier intact in the
// RESULT, which is worth doing even though the arguments arrived already
// rounded: this half is ours to get right and the other half is not.
func succeed(value any) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()

	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		// Unreachable: raw came out of json.Marshal, so it is valid JSON and
		// decoding it into an any cannot fail. The check stays because
		// discarding an error to satisfy a coverage number is the worse trade,
		// and it is named in scripts/coverage-exclusions.txt for that reason.
		return nil, err
	}

	if fields, ok := decoded.(map[string]any); ok {
		return fields, nil
	}
	// Not an object. A spec may legitimately return a string or a number, and
	// adk-go's own functiontool wraps those under "result" rather than refusing
	// them, so this does too.
	return map[string]any{resultKey: decoded}, nil
}

// sortedFields orders the field names so a model sees the same list twice for
// the same failure. Go randomises map iteration, and an LLM shown two different
// orderings of one error has been given two different errors.
func sortedFields(e *services.ValidationError) []string {
	fields := e.FieldMap()
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
