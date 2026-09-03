package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

var nullLiteral = []byte("null")

// Result is what a dispatch resolved, before any wire touches it. It is
// transport-neutral on purpose: ordering, pagination and the response envelope
// are the adapter's business.
type Result struct {
	// Value is the service's return value.
	Value any

	// Status is the spec's success hint. An adapter may map it or ignore it.
	Status int

	// Input is the decoded, validated input, so an adapter can key a decision
	// on what was validated without decoding a second time.
	Input any
}

// Dispatch runs the named spec.
//
// The order below is the contract, and two positions in it are load-bearing:
//
//   - Decoding and both non-authorisation validation layers run BEFORE any
//     transaction is opened, so an invalid payload never costs one.
//   - Dependencies resolve INSIDE the transaction, so Deps holds the
//     transactional handle and a service physically cannot write outside its
//     own boundary. Resolving first and running the service inside looks
//     identical, passes every happy-path test, and writes half the mutation
//     outside the transaction on rollback.
//
// raw is the whole client input as JSON. A nil or empty raw is treated as an
// empty object, so a no-argument spec needs no body.
func (r *Registry[D]) Dispatch(
	ctx context.Context, principal any, name string, raw json.RawMessage,
) (Result, error) {
	e, ok := r.entries[name]
	if !ok {
		return Result{}, fmt.Errorf("%w: no spec named %q", ErrNotFound, name)
	}

	// Absent, empty and explicitly null all mean the same thing: no arguments
	// were sent. Only the first two were treated that way, so a client that
	// renders "no arguments" as a JSON null -- which MCP clients do -- was
	// refused with a schema error naming a type rather than a field, which is
	// nothing a caller can act on.
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), nullLiteral) {
		raw = json.RawMessage("{}")
	}

	// Layer one, on the JSON value rather than the Go value: jsonschema-go
	// cannot validate against a struct, so the payload is parsed twice --
	// once shapelessly to check it, once into In to use it.
	probe, err := decodeJSONValue(raw, false)
	if err != nil {
		return Result{}, malformedBody(err)
	}
	if err := e.input.Validate(probe); err != nil {
		// The validator reports a path rather than a field, and parsing its
		// prose to attribute a message to a field would be a guess. Layer two
		// is where per-field messages come from.
		return Result{}, &ValidationError{
			Fields: map[string][]string{NonFieldKey: {err.Error()}},
		}
	}

	// Layer two.
	decoded, err := e.decode(raw)
	if err != nil {
		return Result{}, err
	}

	var value any
	run := func(ctx context.Context) error {
		deps, err := r.deps(ctx, principal)
		if err != nil {
			return err
		}
		v, err := e.call(Ctx[D]{Context: ctx, Deps: deps}, decoded)
		if err != nil {
			return err
		}
		value = v
		return nil
	}

	if e.atomic && r.atomic != nil {
		err = r.atomic(ctx, run)
	} else {
		err = run(ctx)
	}
	if err != nil {
		return Result{}, err
	}
	return Result{Value: value, Status: e.status, Input: decoded}, nil
}

// DispatchValue is Dispatch for a caller that already holds decoded arguments
// rather than bytes, such as a command line that assembled them from flags.
//
// Prefer Dispatch whenever the arguments arrived as JSON, and reach for this
// only when the map was built in Go.
//
// The reason is that a map[string]any produced by unmarshalling JSON has
// already lost information: encoding/json decodes every number into a float64,
// so an integer beyond 2^53 is silently rounded before it ever reaches here.
// The identifier 9007199254740993 arrives at a service as ...992, with a nil
// error and nothing to indicate a substitution happened. Handing Dispatch the
// original bytes decodes straight into the input type and keeps it exact.
//
// This method previously claimed to be the shape the MCP SDK hands a
// non-generic tool handler. That was wrong: the SDK's CallToolParamsRaw
// carries Arguments as a json.RawMessage, so an MCP adapter has the bytes and
// should use Dispatch.
func (r *Registry[D]) DispatchValue(
	ctx context.Context, principal any, name string, args map[string]any,
) (Result, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return Result{}, &ValidationError{
			Fields: map[string][]string{NonFieldKey: {"unencodable arguments: " + err.Error()}},
		}
	}
	return r.Dispatch(ctx, principal, name, raw)
}

// deps resolves the per-call dependency value. A Registry built with a nil
// resolver yields the zero D, which is correct only when D carries nothing.
func (r *Registry[D]) deps(ctx context.Context, principal any) (D, error) {
	if r.resolve == nil {
		var zero D
		return zero, nil
	}
	return r.resolve(ctx, principal)
}
