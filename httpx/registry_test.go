package httpx_test

import (
	"context"
	"fmt"
	"math"
	"strings"

	services "github.com/Artui/go-services"
)

// The fixture registry every test in this package mounts.
//
// It is one registry rather than a per-test one because a Registry is read-only
// once built, and because a shared one keeps each test to the request it is
// actually about.

// app is the per-call dependency. Identity lives on it rather than on Ctx,
// which is the kernel's arrangement -- see services.New.
type app struct{ viewer string }

// resolve turns the opaque principal the adapter authenticated into app.
//
// The three arms are all reachable from a test: a nil Principal function
// dispatches nil, a Principal returning a string is the ordinary case, and one
// returning anything else is refused -- which is how a resolver says "I do not
// recognise this caller" without the adapter having to know what a caller is.
func resolve(_ context.Context, principal any) (app, error) {
	switch p := principal.(type) {
	case nil:
		return app{viewer: "anonymous"}, nil
	case string:
		return app{viewer: p}, nil
	default:
		return app{}, fmt.Errorf("%w: unrecognised principal", services.ErrPermission)
	}
}

type authorIn struct {
	ID      int64 `json:"id"`
	Verbose bool  `json:"verbose,omitempty"`
}

type authorOut struct {
	ID      int64  `json:"id"`
	Viewer  string `json:"viewer"`
	Verbose bool   `json:"verbose"`
}

type listIn struct {
	Limit int      `json:"limit,omitempty"`
	Tags  []string `json:"tags,omitempty"`
}

type listOut struct {
	Limit  int      `json:"limit"`
	Tags   []string `json:"tags"`
	Viewer string   `json:"viewer"`
}

type writeIn struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Validate is the kernel's second validation layer, and it is here so that a
// 400 with per-field messages has a real producer rather than a hand-built
// error.
func (in writeIn) Validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return services.Invalid("name", "must not be blank")
	}
	return nil
}

type writeOut struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Viewer string `json:"viewer"`
}

type emptyIn struct{}

// fieldlessIn returns a ValidationError carrying no fields at all, which the
// kernel never produces but a hand-written Validate can.
type fieldlessIn struct{}

func (fieldlessIn) Validate() error { return &services.ValidationError{} }

func newRegistry() *services.Registry[app] {
	reg := services.New(resolve)

	services.MustRegister(reg, services.Spec[app, authorIn, authorOut]{
		Name: "get_author",
		Kind: services.Query,
		Run: func(c services.Ctx[app], in authorIn) (authorOut, error) {
			return authorOut{ID: in.ID, Viewer: c.Deps.viewer, Verbose: in.Verbose}, nil
		},
	})

	services.MustRegister(reg, services.Spec[app, listIn, listOut]{
		Name: "list_authors",
		Kind: services.Query,
		Run: func(c services.Ctx[app], in listIn) (listOut, error) {
			return listOut{Limit: in.Limit, Tags: in.Tags, Viewer: c.Deps.viewer}, nil
		},
	})

	services.MustRegister(reg, services.Spec[app, writeIn, writeOut]{
		Name:   "create_author",
		Kind:   services.Mutation,
		Status: 201,
		Run: func(c services.Ctx[app], in writeIn) (writeOut, error) {
			return writeOut{ID: in.ID, Name: in.Name, Viewer: c.Deps.viewer}, nil
		},
	})

	services.MustRegister(reg, services.Spec[app, authorIn, struct{}]{
		Name:   "delete_author",
		Kind:   services.Mutation,
		Status: 204,
		Run:    func(services.Ctx[app], authorIn) (struct{}, error) { return struct{}{}, nil },
	})

	services.MustRegister(reg, services.Spec[app, emptyIn, string]{
		Name: "ping",
		Kind: services.Query,
		Run:  func(services.Ctx[app], emptyIn) (string, error) { return "pong", nil },
	})

	// One spec per error the taxonomy names, each wrapping the sentinel the way
	// a real service would rather than returning it bare, so the tests prove
	// errors.Is is doing the work.
	services.MustRegister(reg, services.Spec[app, emptyIn, string]{
		Name: "gone",
		Kind: services.Query,
		Run: func(services.Ctx[app], emptyIn) (string, error) {
			return "", fmt.Errorf("author 42: %w", services.ErrNotFound)
		},
	})

	services.MustRegister(reg, services.Spec[app, emptyIn, string]{
		Name: "taken",
		Kind: services.Query,
		Run: func(services.Ctx[app], emptyIn) (string, error) {
			return "", fmt.Errorf("slug already used: %w", services.ErrConflict)
		},
	})

	services.MustRegister(reg, services.Spec[app, emptyIn, string]{
		Name: "refused",
		Kind: services.Query,
		Run: func(services.Ctx[app], emptyIn) (string, error) {
			return "", fmt.Errorf("not an editor: %w", services.ErrPermission)
		},
	})

	services.MustRegister(reg, services.Spec[app, emptyIn, string]{
		Name: "exploded",
		Kind: services.Query,
		Run: func(services.Ctx[app], emptyIn) (string, error) {
			return "", fmt.Errorf("dial tcp 10.0.0.7:5432: connection refused")
		},
	})

	// A value the encoder cannot represent. The schema for float64 is a plain
	// number, so this registers and dispatches happily and only fails on the
	// way out -- which is the whole point of the case.
	services.MustRegister(reg, services.Spec[app, emptyIn, float64]{
		Name: "unencodable",
		Kind: services.Query,
		Run:  func(services.Ctx[app], emptyIn) (float64, error) { return math.NaN(), nil },
	})

	services.MustRegister(reg, services.Spec[app, fieldlessIn, string]{
		Name: "fieldless",
		Kind: services.Query,
		Run:  func(services.Ctx[app], fieldlessIn) (string, error) { return "", nil },
	})

	return reg
}
