// Package conformance holds one spec set and drives it through every adapter,
// so that a difference between two transports fails a test rather than reaching
// a client.
//
// It is a test harness and is never published. Nothing imports it.
package conformance

import (
	"context"
	"errors"
	"math"
	"strings"

	services "github.com/Artui/go-services"
)

// SecretText is what an unexpected error says. No transport may put it on a
// wire: an internal error's words are written for an operator.
const SecretText = "a detail written for an operator, not for a client"

// Deps is deliberately empty. This suite is about what the transports do with a
// dispatch, not about what a service does with its dependencies.
type Deps struct{}

// AuthorIn is the ordinary shape: one required field, one optional, one number
// wide enough to catch a float64 round trip.
type AuthorIn struct {
	Name string `json:"name" jsonschema:"the author's display name"`
	Bio  string `json:"bio,omitempty"`
	ID   int64  `json:"id,omitempty"`
}

// Validate is layer two, so every transport has a business rule to disagree on.
func (in AuthorIn) Validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return services.Invalid("name", "must not be blank")
	}
	return nil
}

// AuthorOut is the success shape every transport has to render identically.
type AuthorOut struct {
	Name string `json:"name"`
	Bio  string `json:"bio"`
	ID   int64  `json:"id"`
}

// PatchIn exercises the omitted-versus-zero distinction across transports.
type PatchIn struct {
	Name services.Optional[string]  `json:"name,omitzero"`
	Bio  services.Optional[*string] `json:"bio,omitzero"`
}

// PatchOut reports what the operation actually received, so the
// omitted-versus-null distinction is observable from the wire.
type PatchOut struct {
	NameSet bool `json:"name_set"`
	BioSet  bool `json:"bio_set"`
	BioNil  bool `json:"bio_nil"`
}

// Empty is the input of a spec that takes no arguments.
type Empty struct{}

// Unencodable holds a value encoding/json refuses. Returning one is not
// contrived: an average over an empty set is NaN, and it is how a transport
// that commits a status before marshalling gets caught.
type Unencodable struct {
	Mean float64 `json:"mean"`
}

// Registry builds the spec set every adapter is mounted with.
func Registry() *services.Registry[Deps] {
	r := services.New(func(context.Context, any) (Deps, error) { return Deps{}, nil })

	services.MustRegister(r, services.Spec[Deps, AuthorIn, AuthorOut]{
		Name: "create_author", Kind: services.Mutation, Status: 201,
		Run: func(_ services.Ctx[Deps], in AuthorIn) (AuthorOut, error) {
			return AuthorOut(in), nil
		},
	})

	services.MustRegister(r, services.Spec[Deps, AuthorIn, AuthorOut]{
		Name: "get_author", Kind: services.Query,
		Run: func(_ services.Ctx[Deps], in AuthorIn) (AuthorOut, error) {
			return AuthorOut{Name: in.Name, ID: in.ID}, nil
		},
	})

	services.MustRegister(r, services.Spec[Deps, PatchIn, PatchOut]{
		Name: "patch_author", Kind: services.Mutation,
		Run: func(_ services.Ctx[Deps], in PatchIn) (PatchOut, error) {
			bio, bioSet := in.Bio.Get()
			return PatchOut{NameSet: in.Name.IsSet(), BioSet: bioSet, BioNil: bio == nil}, nil
		},
	})

	services.MustRegister(r, services.Spec[Deps, Empty, AuthorOut]{
		Name: "no_args", Kind: services.Query,
		Run: func(services.Ctx[Deps], Empty) (AuthorOut, error) {
			return AuthorOut{Name: "nobody"}, nil
		},
	})

	// One spec per taxonomy member, so every transport has to answer each.
	for name, err := range map[string]error{
		"denied":  services.ErrPermission,
		"missing": services.ErrNotFound,
		"clash":   services.ErrConflict,
	} {
		services.MustRegister(r, services.Spec[Deps, AuthorIn, AuthorOut]{
			Name: name, Kind: services.Mutation,
			Run: func(services.Ctx[Deps], AuthorIn) (AuthorOut, error) {
				return AuthorOut{}, err
			},
		})
	}

	services.MustRegister(r, services.Spec[Deps, AuthorIn, AuthorOut]{
		Name: "boom", Kind: services.Mutation,
		Run: func(services.Ctx[Deps], AuthorIn) (AuthorOut, error) {
			return AuthorOut{}, errors.New(SecretText)
		},
	})

	// A typed nil in an error position. errors.As matches it, so an adapter
	// that renders without a nil check dereferences it -- which on a transport
	// whose handler runs unrecovered ends the process rather than the request.
	services.MustRegister(r, services.Spec[Deps, AuthorIn, AuthorOut]{
		Name: "typed_nil", Kind: services.Mutation,
		Run: func(services.Ctx[Deps], AuthorIn) (AuthorOut, error) {
			var invalid *services.ValidationError
			return AuthorOut{}, invalid
		},
	})

	// A success value that cannot be marshalled. An adapter committing its
	// status before encoding answers 200 with an empty body here.
	services.MustRegister(r, services.Spec[Deps, AuthorIn, Unencodable]{
		Name: "unencodable", Kind: services.Query,
		Run: func(services.Ctx[Deps], AuthorIn) (Unencodable, error) {
			return Unencodable{Mean: math.NaN()}, nil
		},
	})

	return r
}
