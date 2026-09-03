package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type txKey struct{}

type strictIn struct {
	Name string `json:"name"`
}

func (in strictIn) Validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return Invalid("name", "must not be blank")
	}
	return nil
}

func TestDispatchUnknownSpec(t *testing.T) {
	r := newTestRegistry(t)
	_, err := r.Dispatch(context.Background(), nil, "nope", nil)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestDispatchValidationLayers(t *testing.T) {
	r := newTestRegistry(t)
	MustRegister(r, Spec[testDeps, strictIn, greetOut]{
		Name: "greet", Kind: Query,
		Run: func(_ Ctx[testDeps], in strictIn) (greetOut, error) {
			return greetOut{Greeting: "hi " + in.Name}, nil
		},
	})

	for _, tc := range []struct {
		name, body, want string
	}{
		{"malformed JSON", `{`, NonFieldKey},
		{"layer one: missing required", `{}`, NonFieldKey},
		{"layer one: wrong type", `{"name":123}`, NonFieldKey},
		{"layer one: unknown field", `{"name":"a","surprise":1}`, NonFieldKey},
		{"layer two: business rule", `{"name":"   "}`, "name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Dispatch(context.Background(), nil, "greet", []byte(tc.body))
			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("got %v, want a ValidationError", err)
			}
			if _, ok := invalid.Fields[tc.want]; !ok {
				t.Errorf("fields %v, want a %q key", invalid.Fields, tc.want)
			}
		})
	}
}

func TestDispatchSuccess(t *testing.T) {
	r := newTestRegistry(t)
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{
		Name: "greet", Kind: Query, Status: 201, Run: greet,
	})
	res, err := r.Dispatch(context.Background(), nil, "greet", []byte(`{"name":"ada"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Value.(greetOut).Greeting; got != "hi ada" {
		t.Errorf("Value = %q, want %q", got, "hi ada")
	}
	if res.Status != 201 {
		t.Errorf("Status = %d, want 201", res.Status)
	}
	if got := res.Input.(greetIn).Name; got != "ada" {
		t.Errorf("Input = %+v, want the decoded input", res.Input)
	}
}

func TestDispatchEmptyBodyIsAnEmptyObject(t *testing.T) {
	type noArgs struct{}
	r := newTestRegistry(t)
	MustRegister(r, Spec[testDeps, noArgs, greetOut]{
		Name: "ping", Kind: Query,
		Run: func(Ctx[testDeps], noArgs) (greetOut, error) {
			return greetOut{Greeting: "pong"}, nil
		},
	})
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{Name: "greet", Kind: Query, Run: greet})
	for _, raw := range [][]byte{nil, {}} {
		if _, err := r.Dispatch(context.Background(), nil, "ping", raw); err != nil {
			t.Errorf("a no-argument spec must need no body, got %v", err)
		}
	}
}

func TestDispatchPermitAborts(t *testing.T) {
	r := newTestRegistry(t)
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{
		Name: "greet", Kind: Query, Run: greet,
		Permit: []func(Ctx[testDeps], greetIn) error{
			func(Ctx[testDeps], greetIn) error { return nil },
			func(Ctx[testDeps], greetIn) error { return ErrPermission },
		},
	})
	_, err := r.Dispatch(context.Background(), nil, "greet", []byte(`{"name":"ada"}`))
	if !errors.Is(err, ErrPermission) {
		t.Errorf("got %v, want ErrPermission", err)
	}
}

func TestDispatchServiceErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	r := newTestRegistry(t)
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{
		Name: "greet", Kind: Query,
		Run: func(Ctx[testDeps], greetIn) (greetOut, error) { return greetOut{}, boom },
	})
	if _, err := r.Dispatch(context.Background(), nil, "greet", []byte(`{"name":"a"}`)); !errors.Is(err, boom) {
		t.Errorf("got %v, want the service's own error", err)
	}
}

// The load-bearing ordering rule: dependencies resolve INSIDE the transaction,
// so Deps holds the transactional handle. Resolving first and running the
// service inside looks identical and passes every happy-path test, but writes
// half the mutation outside the boundary on rollback.
func TestDispatchResolvesDepsInsideTheTransaction(t *testing.T) {
	var sawTx bool
	r := New(
		func(ctx context.Context, _ any) (testDeps, error) {
			sawTx = ctx.Value(txKey{}) != nil
			return testDeps{}, nil
		},
		WithAtomic[testDeps](func(ctx context.Context, body func(context.Context) error) error {
			return body(context.WithValue(ctx, txKey{}, "open"))
		}),
	)
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{Name: "w", Kind: Mutation, Run: greet})

	if _, err := r.Dispatch(context.Background(), nil, "w", []byte(`{"name":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if !sawTx {
		t.Error("the resolver must run inside the transaction, not before it")
	}
}

func TestDispatchAtomicity(t *testing.T) {
	newRegistry := func(trace *[]string) *Registry[testDeps] {
		return New(
			func(context.Context, any) (testDeps, error) { return testDeps{}, nil },
			WithAtomic[testDeps](func(ctx context.Context, body func(context.Context) error) error {
				*trace = append(*trace, "begin")
				if err := body(ctx); err != nil {
					*trace = append(*trace, "rollback")
					return err
				}
				*trace = append(*trace, "commit")
				return nil
			}),
		)
	}

	t.Run("a mutation commits", func(t *testing.T) {
		var trace []string
		r := newRegistry(&trace)
		MustRegister(r, Spec[testDeps, greetIn, greetOut]{Name: "w", Kind: Mutation, Run: greet})
		if _, err := r.Dispatch(context.Background(), nil, "w", []byte(`{"name":"a"}`)); err != nil {
			t.Fatal(err)
		}
		if strings.Join(trace, ",") != "begin,commit" {
			t.Errorf("trace = %v, want begin,commit", trace)
		}
	})

	t.Run("a failing mutation rolls back", func(t *testing.T) {
		var trace []string
		r := newRegistry(&trace)
		MustRegister(r, Spec[testDeps, greetIn, greetOut]{
			Name: "w", Kind: Mutation,
			Run: func(Ctx[testDeps], greetIn) (greetOut, error) {
				return greetOut{}, ErrConflict
			},
		})
		_, err := r.Dispatch(context.Background(), nil, "w", []byte(`{"name":"a"}`))
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("got %v, want ErrConflict", err)
		}
		if strings.Join(trace, ",") != "begin,rollback" {
			t.Errorf("trace = %v, want begin,rollback", trace)
		}
	})

	t.Run("a query opens nothing", func(t *testing.T) {
		var trace []string
		r := newRegistry(&trace)
		MustRegister(r, Spec[testDeps, greetIn, greetOut]{Name: "q", Kind: Query, Run: greet})
		if _, err := r.Dispatch(context.Background(), nil, "q", []byte(`{"name":"a"}`)); err != nil {
			t.Fatal(err)
		}
		if len(trace) != 0 {
			t.Errorf("trace = %v, want no transaction for a query", trace)
		}
	})

	t.Run("Atomic overrides the Kind default", func(t *testing.T) {
		var trace []string
		r := newRegistry(&trace)
		off := false
		on := true
		MustRegister(r, Spec[testDeps, greetIn, greetOut]{
			Name: "w", Kind: Mutation, Atomic: &off, Run: greet})
		MustRegister(r, Spec[testDeps, greetIn, greetOut]{
			Name: "q", Kind: Query, Atomic: &on, Run: greet})

		if _, err := r.Dispatch(context.Background(), nil, "w", []byte(`{"name":"a"}`)); err != nil {
			t.Fatal(err)
		}
		if len(trace) != 0 {
			t.Errorf("Atomic=false must suppress the transaction, trace = %v", trace)
		}
		if _, err := r.Dispatch(context.Background(), nil, "q", []byte(`{"name":"a"}`)); err != nil {
			t.Fatal(err)
		}
		if strings.Join(trace, ",") != "begin,commit" {
			t.Errorf("Atomic=true must open one, trace = %v", trace)
		}
	})

	t.Run("an atomic spec without WithAtomic still runs", func(t *testing.T) {
		r := newTestRegistry(t)
		MustRegister(r, Spec[testDeps, greetIn, greetOut]{Name: "w", Kind: Mutation, Run: greet})
		if _, err := r.Dispatch(context.Background(), nil, "w", []byte(`{"name":"a"}`)); err != nil {
			t.Errorf("Atomic is inert without a callback, got %v", err)
		}
	})
}

func TestDispatchResolverError(t *testing.T) {
	r := New(func(context.Context, any) (testDeps, error) { return testDeps{}, ErrPermission })
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{Name: "greet", Kind: Query, Run: greet})
	if _, err := r.Dispatch(context.Background(), nil, "greet", []byte(`{"name":"a"}`)); !errors.Is(err, ErrPermission) {
		t.Errorf("got %v, want the resolver's error", err)
	}
}

func TestDispatchNilResolverYieldsZeroDeps(t *testing.T) {
	r := New[testDeps](nil)
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{
		Name: "greet", Kind: Query,
		Run: func(c Ctx[testDeps], _ greetIn) (greetOut, error) {
			if c.Deps.user != "" {
				return greetOut{}, errors.New("want the zero D")
			}
			return greetOut{Greeting: "ok"}, nil
		},
	})
	if _, err := r.Dispatch(context.Background(), nil, "greet", []byte(`{"name":"a"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchPassesThePrincipalToTheResolver(t *testing.T) {
	type principal struct{ name string }
	r := New(func(_ context.Context, p any) (testDeps, error) {
		// The one place an application asserts its own identity type.
		return testDeps{user: p.(principal).name}, nil
	})
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{
		Name: "greet", Kind: Query,
		Run: func(c Ctx[testDeps], _ greetIn) (greetOut, error) {
			return greetOut{Greeting: c.Deps.user}, nil
		},
	})
	res, err := r.Dispatch(context.Background(), principal{name: "ada"}, "greet", []byte(`{"name":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Value.(greetOut).Greeting; got != "ada" {
		t.Errorf("the typed user did not reach the service, got %q", got)
	}
}

func TestDispatchValue(t *testing.T) {
	r := newTestRegistry(t)
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{Name: "greet", Kind: Query, Run: greet})

	res, err := r.DispatchValue(context.Background(), nil, "greet", map[string]any{"name": "ada"})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Value.(greetOut).Greeting; got != "hi ada" {
		t.Errorf("Value = %q", got)
	}

	// The shape the MCP SDK hands a non-generic handler can still contain
	// something unencodable, and that has to be a validation error, not a panic.
	_, err = r.DispatchValue(context.Background(), nil, "greet", map[string]any{"name": make(chan int)})
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Errorf("got %v, want a ValidationError", err)
	}
}

// The two-parse design has a real failure mode, and this is it: a type whose
// schema is permissive but whose decoder is strict. time.Time advertises as a
// plain string, so "nonsense" satisfies layer one and still fails to decode.
// Without the guard in the decode closure that is a nil dereference downstream.
func TestDispatchDecodeCanFailAfterSchemaValidation(t *testing.T) {
	type timedIn struct {
		At time.Time `json:"at"`
	}
	r := newTestRegistry(t)
	MustRegister(r, Spec[testDeps, timedIn, greetOut]{
		Name: "at", Kind: Query,
		Run: func(Ctx[testDeps], timedIn) (greetOut, error) { return greetOut{}, nil },
	})

	e, _ := r.Lookup("at")
	if got := e.Input.Properties["at"].Type; got != "string" {
		t.Fatalf("time.Time advertises %q; this test assumes string", got)
	}

	_, err := r.Dispatch(context.Background(), nil, "at", []byte(`{"at":"nonsense"}`))
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("got %v, want a ValidationError", err)
	}
	if _, ok := invalid.Fields[NonFieldKey]; !ok {
		t.Errorf("fields %v, want a %q key", invalid.Fields, NonFieldKey)
	}
}

// The sharp edge DispatchValue documents, pinned so it cannot go stale.
//
// A map[string]any built by unmarshalling JSON has already rounded every
// integer past 2^53 to a float64, and nothing reports it. Dispatch takes the
// client's own bytes and decodes straight into the input type, so it does not.
func TestDispatchValueRoundsWhatDispatchKeepsExact(t *testing.T) {
	type idIn struct {
		N int64 `json:"n"`
	}
	var seen int64
	r := newTestRegistry(t)
	MustRegister(r, Spec[testDeps, idIn, greetOut]{
		Name: "id", Kind: Query,
		Run: func(_ Ctx[testDeps], in idIn) (greetOut, error) {
			seen = in.N
			return greetOut{}, nil
		},
	})

	const exact int64 = 9007199254740993
	raw := []byte(`{"n":9007199254740993}`)

	if _, err := r.Dispatch(context.Background(), nil, "id", raw); err != nil {
		t.Fatal(err)
	}
	if seen != exact {
		t.Errorf("Dispatch gave the service %d, want %d exactly", seen, exact)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, err := r.DispatchValue(context.Background(), nil, "id", decoded); err != nil {
		t.Fatal(err)
	}
	if seen == exact {
		t.Skip("encoding/json no longer rounds through any; the doc comment can be softened")
	}
	if seen != 9007199254740992 {
		t.Errorf("DispatchValue gave %d; the documented rounding has changed", seen)
	}
}

// Absent, empty and explicitly null all mean "no arguments were sent". Only the
// first two were treated that way, so an MCP client rendering no arguments as a
// JSON null was refused with a schema error naming a type rather than a field.
func TestDispatchAcceptsNullArguments(t *testing.T) {
	type noArgs struct{}
	r := newTestRegistry(t)
	MustRegister(r, Spec[testDeps, noArgs, greetOut]{
		Name: "ping", Kind: Query,
		Run: func(Ctx[testDeps], noArgs) (greetOut, error) {
			return greetOut{Greeting: "pong"}, nil
		},
	})
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{Name: "greet", Kind: Query, Run: greet})

	for _, raw := range []string{``, `null`, ` null `, `{}`} {
		if _, err := r.Dispatch(context.Background(), nil, "ping", []byte(raw)); err != nil {
			t.Errorf("Dispatch(%q) = %v, want no error", raw, err)
		}
	}

	// A null is only "no arguments" when there are none to send. It must not
	// become an escape from a spec that requires them.
	if _, err := r.Dispatch(context.Background(), nil, "greet", []byte(`null`)); err == nil {
		t.Error("null must not satisfy a spec with required fields")
	}
}
