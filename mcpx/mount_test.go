package mcpx_test

// Wiring failures. Every one of these is a configuration bug, and the kernel's
// position on configuration bugs is that they surface where they were made
// rather than at the first request that trips over them.

import (
	"strings"
	"testing"

	"github.com/Artui/go-services"
	"github.com/Artui/go-services/mcpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// notAnObjectIn is a slice, so its reflected schema declares an array. A spec
// may legitimately take one; an MCP tool may not.
type notAnObjectIn []string

func TestMountRefusesASpecItCannotPublish(t *testing.T) {
	for _, tc := range []struct {
		name     string
		register func(*services.Registry[deps]) error
		want     string
	}{
		{
			name: "a name with a character MCP does not allow",
			register: func(reg *services.Registry[deps]) error {
				return services.Register(reg, services.Spec[deps, empty, empty]{
					Name: "get author",
					Kind: services.Query,
					Run:  func(services.Ctx[deps], empty) (empty, error) { return empty{}, nil },
				})
			},
			want: `an MCP tool name holds only letters, digits, underscore, hyphen and dot, and ' ' is none of those`,
		},
		{
			name: "a name longer than the protocol allows",
			register: func(reg *services.Registry[deps]) error {
				return services.Register(reg, services.Spec[deps, empty, empty]{
					Name: strings.Repeat("a", 129),
					Kind: services.Query,
					Run:  func(services.Ctx[deps], empty) (empty, error) { return empty{}, nil },
				})
			},
			want: "an MCP tool name must be between 1 and 128 characters",
		},
		{
			name: "an input that is not an object",
			register: func(reg *services.Registry[deps]) error {
				return services.Register(reg, services.Spec[deps, notAnObjectIn, empty]{
					Name: "listy",
					Kind: services.Query,
					Run:  func(services.Ctx[deps], notAnObjectIn) (empty, error) { return empty{}, nil },
				})
			},
			want: `this spec's input declares "null or array"`,
		},
		{
			name: "an input that is a bare scalar",
			register: func(reg *services.Registry[deps]) error {
				return services.Register(reg, services.Spec[deps, string, empty]{
					Name: "scalar",
					Kind: services.Query,
					Run:  func(services.Ctx[deps], string) (empty, error) { return empty{}, nil },
				})
			},
			want: `this spec's input declares "string"`,
		},
		{
			name: "an input that declares no type at all",
			register: func(reg *services.Registry[deps]) error {
				return services.Register(reg, services.Spec[deps, any, empty]{
					Name: "anything",
					Kind: services.Query,
					Run:  func(services.Ctx[deps], any) (empty, error) { return empty{}, nil },
				})
			},
			want: "this spec's input declares no type at all",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := services.New[deps](nil)
			must(t, tc.register(reg))

			srv := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "v1"}, nil)
			err := mcpx.Mount(srv, reg, nil)
			if err == nil {
				t.Fatal("Mount accepted a spec it cannot publish")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Mount: %v\nwant it to mention: %s", err, tc.want)
			}
		})
	}
}

// TestARefusedMountAdvertisesNothing is why Mount checks every entry before
// registering any of them.
//
// AddTool notifies connected clients as it goes and has no counterpart that
// takes a tool back, so a mount that failed halfway would leave whichever tools
// it reached already advertised -- and the caller, holding an error, would have
// no way to know which.
func TestARefusedMountAdvertisesNothing(t *testing.T) {
	reg := services.New[deps](nil)
	must(t, services.Register(reg, services.Spec[deps, empty, empty]{
		Name: "perfectly.fine",
		Kind: services.Query,
		Run:  func(services.Ctx[deps], empty) (empty, error) { return empty{}, nil },
	}))
	must(t, services.Register(reg, services.Spec[deps, empty, empty]{
		Name: "not fine",
		Kind: services.Query,
		Run:  func(services.Ctx[deps], empty) (empty, error) { return empty{}, nil },
	}))

	srv := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "v1"}, nil)
	if err := mcpx.Mount(srv, reg, nil); err == nil {
		t.Fatal("Mount accepted a registry holding a spec it cannot publish")
	}

	if advertised := tools(t, dial(t, srv)); len(advertised) != 0 {
		t.Errorf("a refused mount left %d tools advertised: %v", len(advertised), advertised)
	}
}

// TestAnEmptyRegistryMountsCleanly. Nothing to publish is not a failure, and a
// mount that treated it as one would make an optional registry awkward to wire.
func TestAnEmptyRegistryMountsCleanly(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "v1"}, nil)
	if err := mcpx.Mount(srv, services.New[deps](nil), nil); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if advertised := tools(t, dial(t, srv)); len(advertised) != 0 {
		t.Errorf("an empty registry advertised %d tools", len(advertised))
	}
}

// TestAViewMountsAsItself. ByTag returns a Registry, so a mount can publish a
// subset of the specs an application declares -- the shape an MCP surface
// usually wants, since not every HTTP route belongs in front of a model.
func TestAViewMountsAsItself(t *testing.T) {
	reg := services.New[deps](nil)
	must(t, services.Register(reg, services.Spec[deps, empty, empty]{
		Name: "public.thing",
		Kind: services.Query,
		Tags: []string{"agent"},
		Run:  func(services.Ctx[deps], empty) (empty, error) { return empty{}, nil },
	}))
	must(t, services.Register(reg, services.Spec[deps, empty, empty]{
		Name: "internal.thing",
		Kind: services.Query,
		Run:  func(services.Ctx[deps], empty) (empty, error) { return empty{}, nil },
	}))

	cs := connect(t, reg.ByTag("agent"), nil)
	advertised := tools(t, cs)
	if _, ok := advertised["public.thing"]; !ok {
		t.Error("the tagged spec was not advertised")
	}
	if _, ok := advertised["internal.thing"]; ok {
		t.Error("an untagged spec reached the model")
	}
}
