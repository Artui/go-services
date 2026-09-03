package mcpx_test

// The library's central claim is that the schema a client is shown is the
// schema the kernel enforces. These tests are what makes that a fact rather
// than an intention, and they check it three ways: the bytes on the wire, a
// declaration that only one of the two possible implementations could carry,
// and agreement on payloads at the boundary between accepted and rejected.

import (
	"encoding/json"
	"testing"

	"github.com/Artui/go-services"
	"github.com/google/jsonschema-go/jsonschema"
)

// wireTool is the shape of one tool in a recorded tools/list frame. The schemas
// stay as raw bytes deliberately: decoding them into anything would be exactly
// the normalisation this test exists to avoid.
type wireTool struct {
	Name         string          `json:"name"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
}

// TestAdvertisedSchemaIsByteIdenticalToTheEnforcedOne compares what a client
// received with what the kernel holds, without going through the client's
// decoder.
//
// A weaker version of this test -- marshalling the client's decoded
// map[string]any and comparing that -- would pass even if the mount had
// rebuilt the schema, because both sides would have been through the same
// normalising round trip. Comparing the recorded frame does not: encoding/json
// writes a struct's fields in declaration order, so any schema built by some
// other route would differ in key order even where it agreed in content.
func TestAdvertisedSchemaIsByteIdenticalToTheEnforcedOne(t *testing.T) {
	reg := newRegistry(t)
	cs, tap := tapped(t, reg)

	if _, err := cs.ListTools(t.Context(), nil); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var listing struct {
		Tools []wireTool `json:"tools"`
	}
	if err := json.Unmarshal(tap.lastResult(t), &listing); err != nil {
		t.Fatalf("decoding the recorded tools/list: %v", err)
	}

	advertised := make(map[string]wireTool, len(listing.Tools))
	for _, tool := range listing.Tools {
		advertised[tool.Name] = tool
	}
	entries := reg.Entries()
	if len(advertised) != len(entries) {
		t.Fatalf("advertised %d tools for %d entries", len(advertised), len(entries))
	}

	for _, e := range entries {
		tool, ok := advertised[e.Name]
		if !ok {
			t.Fatalf("%q was registered but not advertised", e.Name)
		}
		for _, part := range []struct {
			what   string
			kernel *jsonschema.Schema
			wire   json.RawMessage
		}{
			{"input", e.Input, tool.InputSchema},
			{"output", e.Output, tool.OutputSchema},
		} {
			enforced, err := json.Marshal(part.kernel)
			if err != nil {
				t.Fatalf("%s: encoding the kernel's %s schema: %v", e.Name, part.what, err)
			}
			if string(enforced) != string(part.wire) {
				t.Errorf(
					"%s: the %s schema on the wire is not the kernel's\n client: %s\n kernel: %s",
					e.Name, part.what, part.wire, enforced,
				)
			}
		}
	}
}

// constrainedIn carries a rule no struct tag can state, so the only way it can
// reach a client is if the mount published the object the kernel enriched.
type constrainedIn struct {
	Limit int `json:"limit"`
}

// TestTheSchemaHookReachesTheClient is the falsification of the whole design.
//
// Spec.Schema mutates the reflected input schema after reflection and before it
// is frozen. An adapter that re-reflected the input type -- which is what the
// SDK's generic AddTool[In, Out] does -- would produce a schema without this
// constraint, advertise that, and validate against it, while the kernel went on
// enforcing the enriched one. Both halves of this test would then disagree: the
// client would be shown no maximum, and a payload the client believed valid
// would be refused.
func TestTheSchemaHookReachesTheClient(t *testing.T) {
	reg := services.New[deps](nil)
	must(t, services.Register(reg, services.Spec[deps, constrainedIn, int]{
		Name: "page",
		Kind: services.Query,
		Schema: func(s *jsonschema.Schema) {
			s.Properties["limit"].Maximum = jsonschema.Ptr(50.0)
		},
		Run: func(_ services.Ctx[deps], in constrainedIn) (int, error) { return in.Limit, nil },
	}))

	cs := connect(t, reg, nil)

	advertised := tools(t, cs)["page"]
	shown, err := json.Marshal(advertised.InputSchema)
	if err != nil {
		t.Fatalf("encoding the advertised schema: %v", err)
	}
	var seen struct {
		Properties struct {
			Limit struct {
				Maximum *float64 `json:"maximum"`
			} `json:"limit"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(shown, &seen); err != nil {
		t.Fatalf("decoding the advertised schema: %v", err)
	}
	if seen.Properties.Limit.Maximum == nil || *seen.Properties.Limit.Maximum != 50 {
		t.Errorf("the enrichment did not reach the client: %s", shown)
	}

	// And the same constraint is what actually refuses a call.
	if res := call(t, cs, "page", map[string]any{"limit": 500}); !res.IsError {
		t.Error("the kernel accepted a value the advertised schema forbids")
	}
	if res := call(t, cs, "page", map[string]any{"limit": 50}); res.IsError {
		t.Errorf("the kernel refused a value the advertised schema allows: %s", text(t, res))
	}
}

// TestTheClientCanPredictEveryVerdict resolves the schema a client was given
// and checks that its verdict on a payload is the mount's verdict on the same
// payload.
//
// Byte equality already proves the two schemas are one object. This proves the
// consequence a caller actually cares about: that a client which validates
// locally before calling never disagrees with the server about what is
// acceptable. The payloads sit on both sides of every constraint the fixture
// declares -- type, required, and the additionalProperties: false that struct
// reflection produces.
func TestTheClientCanPredictEveryVerdict(t *testing.T) {
	cs := connect(t, newRegistry(t), nil)
	advertised := tools(t, cs)["authors.get"]

	shown, err := json.Marshal(advertised.InputSchema)
	if err != nil {
		t.Fatalf("encoding the advertised schema: %v", err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(shown, &schema); err != nil {
		t.Fatalf("decoding the advertised schema: %v", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("resolving the advertised schema: %v", err)
	}

	payloads := []string{
		`{"id": 7}`,
		`{}`,
		`{"id": "seven"}`,
		`{"id": 7, "sort": "name"}`,
		`{"id": null}`,
		`{"id": 7.5}`,
	}
	for _, payload := range payloads {
		var value any
		if err := json.Unmarshal([]byte(payload), &value); err != nil {
			t.Fatalf("%s: %v", payload, err)
		}
		clientAccepts := resolved.Validate(value) == nil

		res := call(t, cs, "authors.get", json.RawMessage(payload))
		serverAccepts := !res.IsError

		if clientAccepts != serverAccepts {
			t.Errorf(
				"%s: the advertised schema accepts=%t but the mount accepts=%t (%s)",
				payload, clientAccepts, serverAccepts, text(t, res),
			)
		}
	}
}
