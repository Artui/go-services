package services

import (
	"fmt"
	"strings"
)

// Kind says whether an operation has side effects.
//
// It is declared rather than inferred from the call site, for the reason
// djangorestframework-services gives for its own SelectorKind: the semantics
// have to survive outside a request, where there is no HTTP method to read it
// off. Kind drives the atomic default, the MCP readOnlyHint annotation, and
// which HTTP methods an adapter will let a route use.
type Kind uint8

const (
	// Query has no side effects. Not atomic by default.
	Query Kind = iota + 1

	// Mutation has side effects. Atomic by default.
	Mutation
)

func (k Kind) String() string {
	switch k {
	case Query:
		return "query"
	case Mutation:
		return "mutation"
	default:
		return "unknown"
	}
}

// valid reports whether k was set to one of the declared constants. The zero
// Kind is not valid: a spec that forgot to say is a configuration bug, and
// defaulting it would silently pick side-effect semantics for the author.
func (k Kind) valid() bool { return k == Query || k == Mutation }

// AllowsMethod reports whether an HTTP adapter may mount this Kind on method,
// returning an error naming the conflict when it may not.
//
// It lives here, in a package that imports no transport, because Kind's whole
// job is to state what an operation does and every HTTP adapter needs the same
// answer. Leaving it to the adapters produced exactly the drift it exists to
// prevent: two of them agreed on two prohibitions and both then accepted a
// Query on DELETE and a Mutation on HEAD.
//
// A Query is safe in the RFC 9110 sense, so it takes only the safe methods. A
// Mutation takes the methods that carry intent to change something. The
// comparison is case-insensitive because a route table written by hand says
// "post" as often as "POST".
func (k Kind) AllowsMethod(method string) error {
	upper := strings.ToUpper(strings.TrimSpace(method))
	switch k {
	case Query:
		switch upper {
		case "GET", "HEAD", "OPTIONS":
			return nil
		}
		return fmt.Errorf(
			"services: a query has no side effects and cannot be mounted on %s; "+
				"use GET, HEAD or OPTIONS", upper)
	case Mutation:
		switch upper {
		case "POST", "PUT", "PATCH", "DELETE":
			return nil
		}
		return fmt.Errorf(
			"services: a mutation changes state and cannot be mounted on %s; "+
				"use POST, PUT, PATCH or DELETE", upper)
	}
	return fmt.Errorf("services: %s is not a declared Kind", k)
}
