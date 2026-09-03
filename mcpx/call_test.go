package mcpx_test

// What a client receives when it calls one.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Artui/go-services"
	"github.com/Artui/go-services/mcpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestASuccessCarriesTheSameValueTwice checks that the text block and the
// structured content are one value, not two renderings that could drift.
//
// A client picks one of the two depending on whether it reads output schemas,
// and a mount that summarised one of them would be showing different models
// different answers.
func TestASuccessCarriesTheSameValueTwice(t *testing.T) {
	cs := connect(t, newRegistry(t), nil)

	res := call(t, cs, "authors.get", map[string]any{"id": 7})
	if res.IsError {
		t.Fatalf("call refused: %s", text(t, res))
	}

	var fromText, fromStructured any
	if err := json.Unmarshal([]byte(text(t, res)), &fromText); err != nil {
		t.Fatalf("the text block is not JSON: %v", err)
	}
	restructured, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("re-encoding the structured content: %v", err)
	}
	if err := json.Unmarshal(restructured, &fromStructured); err != nil {
		t.Fatalf("decoding the structured content: %v", err)
	}
	if !reflect.DeepEqual(fromText, fromStructured) {
		t.Errorf("text %v and structured content %v disagree", fromText, fromStructured)
	}

	want := map[string]any{"id": float64(7), "name": "Ursula", "seen_by": "anonymous"}
	if !reflect.DeepEqual(fromText, want) {
		t.Errorf("result %v, want %v", fromText, want)
	}
}

// TestThePrincipalReachesTheService follows one value from the Principal, out
// through the kernel's resolver, into the service's dependencies.
//
// The mount is the only place that could drop it, and dropping it would leave
// every service running as whoever the resolver defaults to -- which is the
// failure mode that looks like nothing at all until an authorisation rule is
// added later.
func TestThePrincipalReachesTheService(t *testing.T) {
	var seen *mcp.CallToolRequest
	principal := func(_ context.Context, req *mcp.CallToolRequest) (any, error) {
		seen = req
		return "ursula", nil
	}
	cs := connect(t, newRegistry(t), principal)

	res := call(t, cs, "authors.get", map[string]any{"id": 1})
	if res.IsError {
		t.Fatalf("call refused: %s", text(t, res))
	}
	if got := text(t, res); !strings.Contains(got, `"seen_by":"ursula"`) {
		t.Errorf("result %s does not carry the principal", got)
	}
	if seen == nil || seen.Params.Name != "authors.get" {
		t.Error("the Principal was not given the request it was authenticating")
	}
}

// TestArgumentsReachTheKernelAsTheySentThem is the reason this adapter calls
// Dispatch rather than DispatchValue.
//
// DispatchValue takes map[string]any, which means decoding the client's bytes
// into interface values first -- and encoding/json decodes every JSON number
// into a float64. An identifier above 2^53 does not survive that. Handing the
// kernel the raw bytes means nothing between the client and the validator ever
// forms an opinion about the arguments.
func TestArgumentsReachTheKernelAsTheySentThem(t *testing.T) {
	const wide = `{"n":9007199254740993}`

	reg := newRegistry(t)
	cs, tap := tapped(t, reg)

	res := call(t, cs, "echo.wide", json.RawMessage(wide))
	if res.IsError {
		t.Fatalf("call refused: %s", text(t, res))
	}
	if got := text(t, res); got != wide {
		t.Errorf("the service saw %s, want %s", got, wide)
	}

	// And the same value on the way back out. This has to be read off the
	// recorded frame: res.StructuredContent has already been through the
	// client's own decoder, which rounds it whatever the server sent.
	var result struct {
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	if err := json.Unmarshal(tap.lastResult(t), &result); err != nil {
		t.Fatalf("decoding the recorded result: %v", err)
	}
	if string(result.StructuredContent) != wide {
		t.Errorf("structured content on the wire %s, want %s", result.StructuredContent, wide)
	}

	// The route not taken, stated so that a future change to DispatchValue
	// gets measured against what it would cost here.
	var asMap map[string]any
	if err := json.Unmarshal([]byte(wide), &asMap); err != nil {
		t.Fatal(err)
	}
	viaMap, err := reg.DispatchValue(t.Context(), nil, "echo.wide", asMap)
	if err != nil {
		t.Fatalf("DispatchValue: %v", err)
	}
	if got := viaMap.Value.(wideIn).N; got == 9007199254740993 {
		t.Error("DispatchValue no longer rounds a wide integer, so this adapter's reason for avoiding it has gone")
	}
}

// TestARefusalIsAResultNotAProtocolError is the contract that keeps a domain
// answer readable.
//
// The MCP specification draws the line at whether the model should see it: a
// tool that ran and declined has produced something the model must read and
// react to, while a tool that does not exist is the client's problem. Getting
// this backwards turns every permission check into what looks like an outage.
func TestARefusalIsAResultNotAProtocolError(t *testing.T) {
	cs := connect(t, newRegistry(t), nil)

	for _, tc := range []struct {
		tool string
		args any
		want string
	}{
		{"fail.permission", map[string]any{"id": 3}, "services: permission denied: author 3 belongs to someone else"},
		{"fail.notfound", map[string]any{"id": 3}, "services: not found: no author 3"},
		{"fail.conflict", map[string]any{"id": 3}, "services: conflict: author 3 still has books"},
		{"fail.permit", map[string]any{}, "services: permission denied: anonymous may not do this"},
	} {
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
		if err != nil {
			t.Errorf("%s: became a protocol error rather than a result: %v", tc.tool, err)
			continue
		}
		if !res.IsError {
			t.Errorf("%s: refusal did not set IsError", tc.tool)
		}
		if got := text(t, res); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.tool, got, tc.want)
		}
	}

	// The other side of the line, for contrast: a tool that does not exist is
	// a protocol error, and the SDK raises it before the mount is involved.
	if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "no.such.tool"}); err == nil {
		t.Error("an unknown tool should be a protocol error")
	}
}

// TestValidationFailuresTellTheModelWhatToFix covers the rendering, since a
// model correcting itself is the only reason to spend words on it.
func TestValidationFailuresTellTheModelWhatToFix(t *testing.T) {
	cs := connect(t, newRegistry(t), nil)

	res := call(t, cs, "fail.validate", map[string]any{"email": "root@"})
	if !res.IsError {
		t.Fatal("a validation failure did not set IsError")
	}
	want := strings.Join([]string{
		"The arguments were rejected. Correct these and call the tool again:",
		"- age: must be at least 18",
		"- email: must contain an at sign",
		"- email: must not be a role address",
		"- an author needs either an email or a phone number",
		"- zzz_sorts_after_them: and the list is sorted",
	}, "\n")
	if got := text(t, res); got != want {
		t.Errorf("rendered\n%s\nwant\n%s", got, want)
	}
}

func TestAValidationFailureThatBlamesNothingStillReads(t *testing.T) {
	cs := connect(t, newRegistry(t), nil)

	res := call(t, cs, "fail.blank", map[string]any{})
	if !res.IsError {
		t.Fatal("a validation failure did not set IsError")
	}
	if got := text(t, res); got != "The arguments were rejected, with no reason given." {
		t.Errorf("rendered %q", got)
	}
}

// TestSchemaRejectionsAreRenderedAsValidationFailures covers the kernel's first
// layer, which attributes its message to the non-field key rather than to a
// property. The rendering drops that key, so the model is not invited to go
// looking for an argument named after it.
func TestSchemaRejectionsAreRenderedAsValidationFailures(t *testing.T) {
	cs := connect(t, newRegistry(t), nil)

	res := call(t, cs, "authors.get", map[string]any{"id": "seven"})
	if !res.IsError {
		t.Fatal("a schema violation was accepted")
	}
	got := text(t, res)
	if !strings.HasPrefix(got, "The arguments were rejected. Correct these and call the tool again:\n- ") {
		t.Errorf("rendered %q", got)
	}
	if strings.Contains(got, services.NonFieldKey) {
		t.Errorf("the non-field key leaked into %q", got)
	}
}

// TestAnUnexpectedFailureIsRedactedAndReported is the two-sided rule: an
// internal error's words go to the operator and a fixed sentence goes to the
// model.
//
// Redacting without reporting would be the worse half of two reasonable
// behaviours, so both sides are asserted in one test rather than one each.
func TestAnUnexpectedFailureIsRedactedAndReported(t *testing.T) {
	type report struct {
		tool string
		err  error
	}
	var reported []report
	reporter := func(_ context.Context, tool string, err error) {
		reported = append(reported, report{tool: tool, err: err})
	}
	cs := connect(t, newRegistry(t), nil, mcpx.WithErrorReporter(reporter))

	res := call(t, cs, "fail.internal", map[string]any{})
	if !res.IsError {
		t.Fatal("an unexpected failure did not set IsError")
	}
	if got := text(t, res); got != mcpx.InternalErrorText {
		t.Errorf("the client was told %q, want the fixed sentence", got)
	}
	if strings.Contains(text(t, res), "10.0.0.7") {
		t.Error("an operator-facing address reached the client")
	}

	if len(reported) != 1 {
		t.Fatalf("the reporter saw %d errors, want 1", len(reported))
	}
	if reported[0].tool != "fail.internal" {
		t.Errorf("the reporter was told tool %q", reported[0].tool)
	}
	if !strings.Contains(reported[0].err.Error(), "connection refused") {
		t.Errorf("the reporter was given %q, not the real error", reported[0].err)
	}
}

// TestRecognisedRefusalsAreNotReported keeps the reporter meaning one thing.
// A refusal the client can already read is not something redaction lost, and a
// reporter that also saw those would be a log of ordinary traffic.
func TestRecognisedRefusalsAreNotReported(t *testing.T) {
	var reported int
	cs := connect(t, newRegistry(t), nil, mcpx.WithErrorReporter(
		func(context.Context, string, error) { reported++ },
	))

	call(t, cs, "fail.permission", map[string]any{"id": 1})
	call(t, cs, "fail.validate", map[string]any{"email": "x"})
	if reported != 0 {
		t.Errorf("the reporter saw %d recognised refusals, want 0", reported)
	}
}

// TestAMountWithoutAReporterStillRedacts. The reporter is optional, and a
// mount that has none must still not put an operator's words on the wire.
func TestAMountWithoutAReporterStillRedacts(t *testing.T) {
	cs := connect(t, newRegistry(t), nil)

	res := call(t, cs, "fail.internal", map[string]any{})
	if got := text(t, res); got != mcpx.InternalErrorText {
		t.Errorf("the client was told %q, want the fixed sentence", got)
	}
}

// TestAnUnencodableResultIsAnInternalFailure covers the one failure that
// happens after the service has already succeeded. It is a bug in the service
// rather than anything the caller did, so it takes the internal path -- and
// the reporter has to see it, because nothing else will.
func TestAnUnencodableResultIsAnInternalFailure(t *testing.T) {
	var reported error
	cs := connect(t, newRegistry(t), nil, mcpx.WithErrorReporter(
		func(_ context.Context, _ string, err error) { reported = err },
	))

	res := call(t, cs, "fail.encode", map[string]any{})
	if !res.IsError {
		t.Fatal("an unencodable result did not set IsError")
	}
	if got := text(t, res); got != mcpx.InternalErrorText {
		t.Errorf("the client was told %q, want the fixed sentence", got)
	}
	var unsupported *json.UnsupportedTypeError
	if !errors.As(reported, &unsupported) {
		t.Errorf("the reporter was given %v, want the encoder's own error", reported)
	}
}

// TestAPrincipalRefusalTravelsTheSameTaxonomy. Authentication is not a special
// case here: what the Principal returns is mapped exactly as a service's error
// would be, which is what lets an application decide whether a caller is told
// why or told nothing.
//
// The alternative -- a JSON-RPC error for an unauthenticated call, by analogy
// with a 401 -- is declined on purpose. MCP puts authentication in front of the
// transport, so a mount reaching for a protocol error here would be the second
// place a refusal is decided, and the two would need to agree forever.
func TestAPrincipalRefusalTravelsTheSameTaxonomy(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{
			name: "an error outside the taxonomy is redacted",
			err:  errors.New("verifying the bearer token against key id 4f2a: read tcp: i/o timeout"),
			want: mcpx.InternalErrorText,
		},
		{
			name: "an error wrapping the taxonomy is readable",
			err:  fmt.Errorf("%w: this connection sent no bearer token", services.ErrPermission),
			want: "services: permission denied: this connection sent no bearer token",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := connect(t, newRegistry(t), func(context.Context, *mcp.CallToolRequest) (any, error) {
				return nil, tc.err
			})
			res := call(t, cs, "authors.get", map[string]any{"id": 1})
			if !res.IsError {
				t.Fatal("a refused principal did not set IsError")
			}
			if got := text(t, res); got != tc.want {
				t.Errorf("%q, want %q", got, tc.want)
			}
		})
	}
}

// TestAResolverRefusalIsMappedToo. The Registry's own resolver runs inside
// Dispatch, after the mount has handed off, so its errors arrive by a different
// route than the Principal's and have to land in the same place.
func TestAResolverRefusalIsMappedToo(t *testing.T) {
	reg := services.New(func(context.Context, any) (deps, error) {
		return deps{}, services.ErrPermission
	})
	must(t, services.Register(reg, services.Spec[deps, empty, empty]{
		Name: "anything",
		Kind: services.Query,
		Run:  func(services.Ctx[deps], empty) (empty, error) { return empty{}, nil },
	}))

	cs := connect(t, reg, nil)
	res := call(t, cs, "anything", map[string]any{})
	if !res.IsError {
		t.Fatal("a refused resolver did not set IsError")
	}
	if got := text(t, res); got != "services: permission denied" {
		t.Errorf("%q", got)
	}
}

// TestATypedNilValidationErrorIsAServerBugNotABadArgument covers the shape that
// took the process down before the kernel and this adapter were both hardened.
//
// A helper whose return type is *services.ValidationError, assigned straight
// into an error, produces a non-nil error holding a nil pointer, and errors.As
// matches it. Rendering that as a validation failure tells a model its
// arguments were wrong and lists nothing, so the only move it has is to retry a
// call that cannot succeed. It is a bug on the server, and it belongs where
// bugs go.
func TestATypedNilValidationErrorIsAServerBugNotABadArgument(t *testing.T) {
	var reported error
	cs := connect(t, newRegistry(t), nil, mcpx.WithErrorReporter(
		func(_ context.Context, _ string, err error) { reported = err },
	))

	res := call(t, cs, "fail.typednil", map[string]any{})
	if !res.IsError {
		t.Fatal("a typed-nil validation error did not set IsError")
	}
	if got := text(t, res); got != mcpx.InternalErrorText {
		t.Errorf("the client was told %q, want the fixed sentence", got)
	}
	if reported == nil {
		t.Error("a server bug reached the client without reaching the reporter")
	}
}

// TestACancelledCallIsNotAnInternalFailure. The reporter exists so that
// redacting an unexpected error does not lose it. A caller going away loses
// nothing, and on a busy mount it is the most common thing that happens, so
// classifying it as a fault makes the log useless exactly when it matters.
func TestACancelledCallIsNotAnInternalFailure(t *testing.T) {
	reported := make(chan error, 4)
	cs := connect(t, newRegistry(t), nil, mcpx.WithErrorReporter(
		func(_ context.Context, _ string, err error) { reported <- err },
	))

	// The client gives up while the service is still blocked. The SDK
	// propagates that, so the handler's own context ends.
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "fail.blocks", Arguments: map[string]any{},
	}); err == nil {
		t.Fatal("the client should have given up on this call")
	}

	select {
	case err := <-reported:
		t.Errorf("an ordinary cancellation was reported as a fault: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
}

// TestAContextErrorFromInsideAServiceIsStillAFault is the other half, and the
// reason the classification checks the call's own context rather than only the
// error.
//
// A service whose downstream call timed out returns a context error while this
// request is still perfectly alive. Matching on the error alone would file that
// under "the caller left" and drop it, which is a real fault going quiet.
func TestAContextErrorFromInsideAServiceIsStillAFault(t *testing.T) {
	var reported error
	cs := connect(t, newRegistry(t), nil, mcpx.WithErrorReporter(
		func(_ context.Context, _ string, err error) { reported = err },
	))

	res := call(t, cs, "fail.downstream", map[string]any{})
	if got := text(t, res); got != mcpx.InternalErrorText {
		t.Errorf("the client was told %q, want the fixed sentence", got)
	}
	if reported == nil {
		t.Fatal("a downstream timeout was mistaken for the caller leaving, and was dropped")
	}
	if !strings.Contains(reported.Error(), "billing service") {
		t.Errorf("the reporter was given %q, not the real error", reported)
	}
}

// TestAServerImposedDeadlineTellsTheModelItRanOutOfTime covers the other
// interruption, which arrives by a different route than a client cancellation.
//
// A mount that caps how long a tool may run does it with receiving middleware,
// so the handler's context carries a deadline rather than being cancelled from
// the far end. The client is still listening, so unlike a cancellation it does
// receive the result -- which makes the wording matter: "ran out of time" is
// something a model can act on, where the internal-failure sentence would have
// told it the server is broken and to stop trying.
func TestAServerImposedDeadlineTellsTheModelItRanOutOfTime(t *testing.T) {
	reported := make(chan error, 4)
	srv := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "v1"}, nil)
	err := mcpx.Mount(srv, newRegistry(t), nil, mcpx.WithErrorReporter(
		func(_ context.Context, _ string, err error) { reported <- err },
	))
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}

	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			ctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancel()
			return next(ctx, method, req)
		}
	})

	res := call(t, dial(t, srv), "fail.blocks", map[string]any{})
	if !res.IsError {
		t.Fatal("a call that ran out of time did not set IsError")
	}
	if got := text(t, res); got != mcpx.TimedOutText {
		t.Errorf("the client was told %q, want %q", got, mcpx.TimedOutText)
	}

	select {
	case err := <-reported:
		t.Errorf("a deadline the mount itself imposed was reported as a fault: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
}
