package conformance

import (
	"encoding/json"
	"strings"

	services "github.com/Artui/go-services"
)

// Outcome reduces a transport's answer to the facts every transport can
// express, so that "these two agree" is a question with a meaning.
//
// It deliberately does not carry a status code or an error class. HTTP has
// statuses and MCP does not, and inventing a mapping between them would make
// the comparison assert the mapping rather than the behaviour. What every
// transport can say is: did it fail, what value came back, which fields were
// named, and did anything reach the client that should not have.
type Outcome struct {
	// Failed is the one fact every transport expresses the same way.
	Failed bool

	// Value is the decoded success payload, nil when Failed.
	Value map[string]any

	// Messages are what the failure said, without the key that held them. HTTP
	// carries them in an envelope and MCP as prose, and both are built from the
	// same ValidationError, so equal messages mean the same failure.
	Messages []string

	// Leaked reports that SecretText appeared anywhere in what the client
	// received. It must be false on every transport for every case.
	Leaked bool

	// Wire is the full response as the client saw it. Only the two HTTP
	// adapters are compared on this; they share a wire format, so anything less
	// than byte equality between them is a difference a client can observe.
	Wire string

	// Status is HTTP-only and zero elsewhere. It is compared within the HTTP
	// pair and ignored across transports.
	Status int
}

// MessagesFromJSON flattens an HTTP error envelope into the messages it
// carried, discarding which key held them.
//
// The keys are not comparable across transports and the messages are: both
// envelopes are built from the same *services.ValidationError, so if the two
// transports carry the same messages they are reporting the same failure. An
// earlier version of this compared field names recovered from prose by
// substring, which found "id" inside "validating" and reported a divergence
// that did not exist -- a harness can be wrong in exactly the direction that
// wastes the most time.
func MessagesFromJSON(body []byte) []string {
	var envelope struct {
		Errors map[string][]string `json:"errors"`
		Error  string              `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil
	}
	if envelope.Error != "" {
		return []string{envelope.Error}
	}
	var messages []string
	for field, group := range envelope.Errors {
		for _, message := range group {
			// The field is inlined, because the other transport has no envelope
			// to put a key in and writes "name: must not be blank" as prose.
			// The reserved non-field key means "this belongs to no field", which
			// prose expresses by simply not naming one.
			if field == services.NonFieldKey {
				messages = append(messages, message)
				continue
			}
			messages = append(messages, field+": "+message)
		}
	}
	return messages
}

// MessagesFromText pulls the same messages out of prose addressed to a model,
// which lists them one per line after a dash.
func MessagesFromText(text string) []string {
	var messages []string
	for _, line := range strings.Split(text, "\n") {
		if trimmed, ok := strings.CutPrefix(strings.TrimSpace(line), "- "); ok {
			messages = append(messages, trimmed)
		}
	}
	return messages
}
