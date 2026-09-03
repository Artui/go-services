package services

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
)

// NonFieldKey is the ValidationError key for a message that belongs to the
// payload as a whole rather than to one field.
const NonFieldKey = "non_field_errors"

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

// Registry holds a set of specs under their names and is what every adapter
// reads. D is the per-call dependency type.
type Registry[D any] struct {
	resolve func(context.Context, any) (D, error)
	atomic  func(context.Context, func(context.Context) error) error
	entries map[string]*entry[D]
	order   []string
}

// entry is a registered spec with its types erased. Register builds the two
// closures while In and Out are still known; nothing downstream needs them.
type entry[D any] struct {
	meta   Entry
	input  *jsonschema.Resolved
	atomic bool
	status int

	// decode unmarshals raw into In and runs the Validator layer. It runs
	// before any transaction is opened, so an invalid payload never costs one.
	decode func(json.RawMessage) (any, error)

	// call runs Permit and then Run. Its second argument is always the value
	// decode produced for this same entry.
	call func(Ctx[D], any) (any, error)
}

// Entry is the untyped view of a registered spec: everything an adapter needs
// to advertise an operation, and nothing it needs to run one.
type Entry struct {
	Name        string
	Description string
	Kind        Kind
	Idempotent  *bool
	Status      int
	Tags        []string
	Metadata    map[string]any
	Input       *jsonschema.Schema
	Output      *jsonschema.Schema
}

// Option configures a Registry.
type Option[D any] func(*Registry[D])

// WithAtomic supplies the "run this inside a transaction" callback. The kernel
// names no driver: database/sql, pgx.BeginFunc and GORM's db.Transaction all
// fit this shape.
//
// The callback receives a context it is expected to make transactional. The
// Registry then resolves dependencies with that context, so Deps holds the
// transactional handle -- see Registry.Dispatch for why the ordering matters.
func WithAtomic[D any](fn func(context.Context, func(context.Context) error) error) Option[D] {
	return func(r *Registry[D]) { r.atomic = fn }
}

// New builds a Registry.
//
// resolve turns the opaque principal an adapter authenticated into the
// per-call dependency value. It is the one place an application asserts its own
// identity type; every service and Permit function downstream gets D already
// typed. A nil resolve yields the zero D, which is the right answer only when D
// carries nothing.
func New[D any](resolve func(context.Context, any) (D, error), opts ...Option[D]) *Registry[D] {
	r := &Registry[D]{resolve: resolve, entries: map[string]*entry[D]{}}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Register adds a spec. It is generic; the entry it stores is not.
//
// In and Out are still known here, so both schemas are reflected once and the
// closures capture the concrete types. Everything downstream reads an Entry.
// This is the same erasure mcp.AddTool performs, which is a good sign: the SDK
// had the identical problem and solved it the identical way.
//
// It returns an error rather than deferring to request time, because a
// duplicate name or an unreflectable type is a configuration bug that must not
// reach a request.
func Register[D, In, Out any](r *Registry[D], s Spec[D, In, Out]) error {
	if s.Name == "" {
		return fmt.Errorf("services: a spec must have a name")
	}
	if _, exists := r.entries[s.Name]; exists {
		return fmt.Errorf("services: %q is already registered", s.Name)
	}
	if !s.Kind.valid() {
		return fmt.Errorf("services: %q must declare a Kind", s.Name)
	}
	if s.Run == nil {
		return fmt.Errorf("services: %q has no Run function", s.Name)
	}

	in, err := reflectSchema(reflect.TypeFor[In]())
	if err != nil {
		return fmt.Errorf("services: %q input: %w", s.Name, err)
	}
	if s.Schema != nil {
		s.Schema(in)
	}
	resolved, err := in.Resolve(nil)
	if err != nil {
		return fmt.Errorf("services: %q input schema is invalid: %w", s.Name, err)
	}

	out, err := reflectSchema(reflect.TypeFor[Out]())
	if err != nil {
		return fmt.Errorf("services: %q output: %w", s.Name, err)
	}

	status := s.Status
	if status == 0 {
		status = 200
	}
	atomic := s.Kind == Mutation
	if s.Atomic != nil {
		atomic = *s.Atomic
	}

	r.entries[s.Name] = &entry[D]{
		meta: Entry{
			Name:        s.Name,
			Description: s.Description,
			Kind:        s.Kind,
			Idempotent:  s.Idempotent,
			Status:      status,
			Tags:        slices.Clone(s.Tags),
			Metadata:    s.Metadata,
			Input:       in,
			Output:      out,
		},
		input:  resolved,
		atomic: atomic,
		status: status,
		decode: func(raw json.RawMessage) (any, error) {
			var v In
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, &ValidationError{
					Fields: map[string][]string{NonFieldKey: {err.Error()}},
				}
			}
			if validator, ok := any(v).(Validator); ok {
				if err := validator.Validate(); err != nil {
					return nil, err
				}
			}
			return v, nil
		},
		call: func(c Ctx[D], decoded any) (any, error) {
			// Cannot fail: decode built this value for this same entry, so a
			// mismatch would be a bug in this file rather than a runtime state.
			v := decoded.(In)
			for _, permit := range s.Permit {
				if err := permit(c, v); err != nil {
					return nil, err
				}
			}
			return s.Run(c, v)
		},
	}
	r.order = append(r.order, s.Name)
	return nil
}

// MustRegister is Register for a package-level declaration, where a
// configuration error should stop the program rather than be returned to
// nobody.
func MustRegister[D, In, Out any](r *Registry[D], s Spec[D, In, Out]) {
	if err := Register(r, s); err != nil {
		panic(err)
	}
}

// Entries returns every entry in registration order, so an adapter's listing is
// stable across runs.
func (r *Registry[D]) Entries() []Entry {
	out := make([]Entry, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.entries[name].meta)
	}
	return out
}

// Lookup returns the entry registered under name.
func (r *Registry[D]) Lookup(name string) (Entry, bool) {
	e, ok := r.entries[name]
	if !ok {
		return Entry{}, false
	}
	return e.meta, true
}

// Len returns how many specs are registered.
func (r *Registry[D]) Len() int { return len(r.order) }

// ByTag returns a new Registry holding the entries carrying any of tags.
//
// Union, not intersection -- chain calls for an intersection. The result is a
// snapshot sharing the same entries: a later Register on the source does not
// appear in a view derived earlier.
func (r *Registry[D]) ByTag(tags ...string) *Registry[D] {
	view := &Registry[D]{
		resolve: r.resolve,
		atomic:  r.atomic,
		entries: map[string]*entry[D]{},
	}
	for _, name := range r.order {
		e := r.entries[name]
		if slices.ContainsFunc(e.meta.Tags, func(t string) bool { return slices.Contains(tags, t) }) {
			view.entries[name] = e
			view.order = append(view.order, name)
		}
	}
	return view
}
