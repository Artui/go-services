package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

type testDeps struct {
	user string
}

type greetIn struct {
	Name string `json:"name" jsonschema:"who to greet"`
}

type greetOut struct {
	Greeting string `json:"greeting"`
}

func greet(_ Ctx[testDeps], in greetIn) (greetOut, error) {
	return greetOut{Greeting: "hi " + in.Name}, nil
}

func newTestRegistry(t *testing.T) *Registry[testDeps] {
	t.Helper()
	return New(func(context.Context, any) (testDeps, error) { return testDeps{}, nil })
}

func TestRegisterRejectsMisconfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
		run  func(*Registry[testDeps]) error
	}{
		{"no name", "must have a name", func(r *Registry[testDeps]) error {
			return Register(r, Spec[testDeps, greetIn, greetOut]{Kind: Query, Run: greet})
		}},
		{"no kind", "must declare a Kind", func(r *Registry[testDeps]) error {
			return Register(r, Spec[testDeps, greetIn, greetOut]{Name: "g", Run: greet})
		}},
		{"no run", "has no Run function", func(r *Registry[testDeps]) error {
			return Register(r, Spec[testDeps, greetIn, greetOut]{Name: "g", Kind: Query})
		}},
		{"unreflectable input", "input", func(r *Registry[testDeps]) error {
			type bad struct {
				Ch chan int `json:"ch"`
			}
			return Register(r, Spec[testDeps, bad, greetOut]{
				Name: "g", Kind: Query,
				Run: func(Ctx[testDeps], bad) (greetOut, error) { return greetOut{}, nil },
			})
		}},
		{"unreflectable output", "output", func(r *Registry[testDeps]) error {
			type bad struct {
				Ch chan int `json:"ch"`
			}
			return Register(r, Spec[testDeps, greetIn, bad]{
				Name: "g", Kind: Query,
				Run: func(Ctx[testDeps], greetIn) (bad, error) { return bad{}, nil },
			})
		}},
		{"unresolvable schema", "schema is invalid", func(r *Registry[testDeps]) error {
			return Register(r, Spec[testDeps, greetIn, greetOut]{
				Name: "g", Kind: Query, Run: greet,
				Schema: func(s *jsonschema.Schema) { s.Ref = "#/definitely/not/here" },
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(newTestRegistry(t))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestRegisterRefusesDuplicates(t *testing.T) {
	r := newTestRegistry(t)
	spec := Spec[testDeps, greetIn, greetOut]{Name: "greet", Kind: Query, Run: greet}
	if err := Register(r, spec); err != nil {
		t.Fatal(err)
	}
	// A copy-pasted declaration must fail loudly rather than shadow the first.
	if err := Register(r, spec); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Errorf("got %v, want an already-registered error", err)
	}
}

func TestMustRegisterPanicsOnMisconfiguration(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustRegister must panic when Register would have errored")
		}
	}()
	MustRegister(newTestRegistry(t), Spec[testDeps, greetIn, greetOut]{Kind: Query, Run: greet})
}

func TestMustRegisterSucceedsQuietly(t *testing.T) {
	r := newTestRegistry(t)
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{Name: "greet", Kind: Query, Run: greet})
	if r.Len() != 1 {
		t.Errorf("Len = %d, want 1", r.Len())
	}
}

func TestEntryDefaultsAndCarriedFacts(t *testing.T) {
	r := newTestRegistry(t)
	yes := true
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{
		Name: "greet", Description: "say hi", Kind: Query,
		Idempotent: &yes, Tags: []string{"read"},
		Metadata: map[string]any{"team": "core"},
		Run:      greet,
	})
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{
		Name: "create", Kind: Mutation, Status: 201, Run: greet,
	})

	got, ok := r.Lookup("greet")
	if !ok {
		t.Fatal("greet should be registered")
	}
	if got.Status != 200 {
		t.Errorf("Status = %d, want the 200 default", got.Status)
	}
	if got.Idempotent == nil || !*got.Idempotent {
		t.Error("a declared Idempotent must be carried")
	}
	if got.Metadata["team"] != "core" {
		t.Error("Metadata must be carried verbatim")
	}
	if got.Input == nil || got.Output == nil {
		t.Error("both schemas must be reflected at registration")
	}

	created, _ := r.Lookup("create")
	if created.Status != 201 {
		t.Errorf("Status = %d, want the declared 201", created.Status)
	}
	// nil means undeclared, and must stay distinguishable from a declared false.
	if created.Idempotent != nil {
		t.Error("an undeclared Idempotent must stay nil")
	}
	if created.Destructive != nil {
		t.Error("an undeclared Destructive must stay nil")
	}

	if _, ok := r.Lookup("absent"); ok {
		t.Error("Lookup must report a miss")
	}
}

func TestEntriesKeepRegistrationOrder(t *testing.T) {
	r := newTestRegistry(t)
	for _, name := range []string{"zeta", "alpha", "mu"} {
		MustRegister(r, Spec[testDeps, greetIn, greetOut]{Name: name, Kind: Query, Run: greet})
	}
	var got []string
	for _, e := range r.Entries() {
		got = append(got, e.Name)
	}
	// Registration order, not map order: an adapter's listing has to be stable.
	if strings.Join(got, ",") != "zeta,alpha,mu" {
		t.Errorf("Entries order = %v, want registration order", got)
	}
}

func TestByTagIsAUnionSnapshot(t *testing.T) {
	r := newTestRegistry(t)
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{
		Name: "read", Kind: Query, Tags: []string{"public", "safe"}, Run: greet})
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{
		Name: "write", Kind: Mutation, Tags: []string{"admin"}, Run: greet})
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{
		Name: "untagged", Kind: Query, Run: greet})

	if got := r.ByTag("public", "admin").Len(); got != 2 {
		t.Errorf("union of two tags = %d entries, want 2", got)
	}
	if got := r.ByTag("public").ByTag("safe").Len(); got != 1 {
		t.Errorf("chained tags = %d entries, want 1", got)
	}
	if got := r.ByTag().Len(); got != 0 {
		t.Errorf("no tags = %d entries, want 0", got)
	}

	view := r.ByTag("public")
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{
		Name: "later", Kind: Query, Tags: []string{"public"}, Run: greet})
	if view.Len() != 1 {
		t.Error("a derived view is a snapshot: a later Register must not appear in it")
	}
}

func TestSchemaHookEnrichesTheAdvertisedSchema(t *testing.T) {
	r := newTestRegistry(t)
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{
		Name: "greet", Kind: Query, Run: greet,
		Schema: func(s *jsonschema.Schema) {
			max := 10
			s.Properties["name"].MaxLength = &max
		},
	})
	e, _ := r.Lookup("greet")
	if e.Input.Properties["name"].MaxLength == nil {
		t.Fatal("the hook must reach the advertised schema")
	}

	// And the same object is what the kernel enforces -- there is no second
	// schema that could disagree with what a tool advertises.
	_, err := r.Dispatch(context.Background(), nil, "greet",
		[]byte(`{"name":"a name well over ten characters"}`))
	if err == nil {
		t.Error("the enriched constraint must be enforced, not only published")
	}
}

// Kind cannot answer this: Mutation covers both a create and a delete, and an
// approval gate keyed on MCP's destructiveHint -- which defaults to true --
// over-prompts on every create until the author can say otherwise.
func TestDestructiveIsThreeState(t *testing.T) {
	r := newTestRegistry(t)
	yes, no := true, false
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{
		Name: "delete", Kind: Mutation, Destructive: &yes, Run: greet})
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{
		Name: "create", Kind: Mutation, Destructive: &no, Run: greet})
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{
		Name: "unsaid", Kind: Mutation, Run: greet})

	for name, want := range map[string]*bool{"delete": &yes, "create": &no, "unsaid": nil} {
		e, _ := r.Lookup(name)
		switch {
		case want == nil && e.Destructive != nil:
			t.Errorf("%s: got %v, want undeclared", name, *e.Destructive)
		case want != nil && (e.Destructive == nil || *e.Destructive != *want):
			t.Errorf("%s: got %v, want %v", name, e.Destructive, *want)
		}
	}
}

// An Entry is read by every adapter for the life of the process, and a
// published MCP annotation is read once and cached by clients. A spec's
// declared tri-states must therefore not still be the caller's variable.
func TestRegisterDetachesDeclaredFlags(t *testing.T) {
	r := newTestRegistry(t)
	idempotent, destructive := true, true
	tags := []string{"public"}
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{
		Name: "x", Kind: Mutation,
		Idempotent: &idempotent, Destructive: &destructive, Tags: tags,
		Run: greet,
	})

	idempotent, destructive, tags[0] = false, false, "secret"

	e, _ := r.Lookup("x")
	if !*e.Idempotent || !*e.Destructive {
		t.Error("a later write to the author's bool must not rewrite what was advertised")
	}
	if e.Tags[0] != "public" {
		t.Error("a later write to the author's slice must not rewrite the tags")
	}
}

// The same guarantee Dispatch already gives, moved to configuration time. A
// route table declaring a capture the operation has no field for is broken in
// every request it will ever serve.
func TestEntryCheckCaptures(t *testing.T) {
	r := newTestRegistry(t)
	MustRegister(r, Spec[testDeps, greetIn, greetOut]{Name: "greet", Kind: Query, Run: greet})
	e, _ := r.Lookup("greet")

	if err := e.CheckCaptures(); err != nil {
		t.Errorf("no captures is fine, got %v", err)
	}
	if err := e.CheckCaptures("name"); err != nil {
		t.Errorf("a declared capture is fine, got %v", err)
	}

	err := e.CheckCaptures("tenant", "name", "region")
	if err == nil {
		t.Fatal("undeclared captures must be refused")
	}
	// The same fault Dispatch reports, so it carries the same sentinel and an
	// adapter maps both to 500 without a second rule.
	if !errors.Is(err, ErrConfiguration) {
		t.Errorf("got %v, want ErrConfiguration", err)
	}
	// Named together and in a stable order, so a wrong route table is fixed in
	// one pass rather than one error at a time.
	if !strings.Contains(err.Error(), "region, tenant") {
		t.Errorf("got %q, want both undeclared captures sorted", err)
	}
	if strings.Contains(err.Error(), "name") && !strings.Contains(err.Error(), "greet") {
		t.Errorf("got %q, want the declared capture omitted", err)
	}

	// An Entry with no schema at all cannot receive anything.
	if (Entry{Name: "x"}).CheckCaptures("id") == nil {
		t.Error("an entry with no input schema must refuse every capture")
	}
}

// Status is the spec author's field, so an adapter checking it could only
// refuse a registry another adapter had already accepted. A 1xx is the case
// that matters: it is a real status code, and it is not one a response can end
// with -- the next write commits an implicit 200 behind it.
func TestRegisterRefusesAnUndeliverableStatus(t *testing.T) {
	for _, status := range []int{99, 100, 103, 199, 600} {
		r := newTestRegistry(t)
		err := Register(r, Spec[testDeps, greetIn, greetOut]{
			Name: "x", Kind: Query, Status: status, Run: greet})
		if err == nil {
			t.Errorf("Status %d was accepted; it cannot be sent as a final status", status)
			continue
		}
		if !strings.Contains(err.Error(), "cannot be sent") {
			t.Errorf("Status %d: got %q, want it to say why", status, err)
		}
	}

	// Zero still means "use the default", which is a different fact from a
	// status somebody computed wrongly.
	r := newTestRegistry(t)
	if err := Register(r, Spec[testDeps, greetIn, greetOut]{
		Name: "ok", Kind: Query, Status: 0, Run: greet}); err != nil {
		t.Errorf("zero must remain the default, got %v", err)
	}
	e, _ := r.Lookup("ok")
	if e.Status != 200 {
		t.Errorf("Status = %d, want the 200 default", e.Status)
	}
}
