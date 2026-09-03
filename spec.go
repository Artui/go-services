package services

import "github.com/google/jsonschema-go/jsonschema"

// Spec is the declaration of one operation. It is data: no method on it does
// work, and nothing here knows about HTTP, MCP or a database.
//
// D is the per-call dependency type, In the input and Out the output. Register
// reflects In and Out into JSON Schema once, at registration, so the schema an
// adapter advertises is the schema the kernel enforces.
type Spec[D, In, Out any] struct {
	// Name is the canonical name, unique within a Registry. It is what an MCP
	// tool is called and what an HTTP route is keyed by.
	Name string

	// Description is prose for a human or a model. MCP publishes it.
	Description string

	// Kind says whether this has side effects. Required.
	Kind Kind

	// Run is the operation. Required.
	Run func(Ctx[D], In) (Out, error)

	// Permit is validation layer three: authorisation and state rules that need
	// resolved dependencies. Each runs after decode and schema validation, with
	// Deps resolved and inside the transaction. Raise-to-abort, as in
	// djangorestframework-services: returning nil permits, returning an error
	// aborts, and a predicate that merely returns false does nothing.
	Permit []func(Ctx[D], In) error

	// Atomic overrides Kind's default (Mutation atomic, Query not). It has no
	// effect unless the Registry was built WithAtomic.
	Atomic *bool

	// Idempotent declares whether repeating the call leaves the same state as
	// making it once. Nothing in this package reads it: idempotency is a
	// property of the function the author wrote, not something a dispatcher can
	// arrange. It is here so the fact is stated once and every transport reads
	// the same answer.
	//
	// nil means undeclared, and that is the default. A transport publishing this
	// as an annotation must be able to tell "nothing was said" from a declared
	// false, or every spec ever written starts claiming it is not idempotent.
	Idempotent *bool

	// Destructive declares whether the operation may remove or overwrite
	// existing state, as opposed to only adding to it.
	//
	// Kind cannot answer this: Mutation covers both a create and a delete, and
	// the difference is exactly what an approval gate wants to know. MCP's
	// destructiveHint defaults to TRUE for any non-read-only tool, so without
	// this a pure create is advertised as possibly destructive and every
	// approval policy keyed on that hint over-prompts.
	//
	// nil means undeclared, matching Idempotent: a transport publishing this as
	// an annotation must be able to tell silence from a declared false.
	Destructive *bool

	// Status is an HTTP-ish success hint carried on the Result. Zero means 200.
	Status int

	// Schema enriches the reflected input schema before it is frozen -- the
	// place to add the constraints a struct tag cannot carry, since a jsonschema
	// tag holds a description and nothing else.
	//
	// It mutates the same object the adapters advertise, so there is no code
	// path in which the advertised contract and the enforced contract differ.
	Schema func(*jsonschema.Schema)

	// Tags are free-form labels for selecting a subset of a Registry.
	Tags []string

	// Metadata is consumer-owned and wholly opaque: the kernel carries it and
	// never reads it. No key is reserved.
	Metadata map[string]any
}

// Validator is validation layer two. An input type implementing it has Validate
// called after schema validation and before any transaction is opened, on every
// transport.
//
// It is for format and single-record business rules -- the ones a JSON Schema
// cannot state. Rules needing dependencies or the acting principal belong in
// Spec.Permit, which runs later and sees both.
type Validator interface {
	Validate() error
}
