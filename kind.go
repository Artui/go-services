package services

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
