package aguix_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/aguix"
)

type deps struct{ user string }

func resolve(_ context.Context, principal any) (deps, error) {
	user, ok := principal.(string)
	if !ok || user == "" {
		return deps{}, fmt.Errorf("%w: this run is not signed in", services.ErrPermission)
	}
	return deps{user: user}, nil
}

type borrowIn struct {
	BookID int64 `json:"book_id" jsonschema:"the book to borrow"`
}

func (in borrowIn) Validate() error {
	if in.BookID <= 0 {
		return services.Invalid("book_id", "must be a positive identifier")
	}
	return nil
}

type borrowOut struct {
	LoanID int64  `json:"loan_id"`
	By     string `json:"by"`
}

const operatorSecret = "dial tcp 10.0.0.4:5432: connection refused"

func newRegistry(t *testing.T) *services.Registry[deps] {
	t.Helper()
	reg := services.New(resolve)

	services.MustRegister(reg, services.Spec[deps, borrowIn, borrowOut]{
		Name: "borrow_book", Description: "Lend a copy of a book.",
		Kind: services.Mutation, Status: 201,
		Run: func(c services.Ctx[deps], in borrowIn) (borrowOut, error) {
			switch in.BookID {
			case 13:
				return borrowOut{}, fmt.Errorf(
					"%w: book 13 is reference only", services.ErrPermission)
			case 99:
				return borrowOut{}, fmt.Errorf("%w: no book 99", services.ErrNotFound)
			case 5:
				return borrowOut{}, fmt.Errorf(
					"%w: no copy of book 5 is on the shelf", services.ErrConflict)
			case 7:
				return borrowOut{}, fmt.Errorf("%s", operatorSecret)
			}
			return borrowOut{LoanID: in.BookID * 10, By: c.Deps.user}, nil
		},
	})
	return reg
}

func signedIn(context.Context) (any, error) { return "ada", nil }

func toolboxFor(t *testing.T, principal aguix.Principal) *aguix.Toolbox[deps] {
	t.Helper()
	box, err := aguix.NewToolbox(newRegistry(t), principal)
	if err != nil {
		t.Fatalf("NewToolbox: %v", err)
	}
	return box
}

// runTool drives one scripted tool call and returns the frames it produced.
func runTool(t *testing.T, box *aguix.Toolbox[deps], args string) []map[string]any {
	t.Helper()
	return frames(t, toolStream(t, box, args))
}

// toolStream is the same run left as the bytes that went out.
//
// The frame tests read this rather than the parsed maps, because decoding into
// a map has already thrown away the one thing they assert: which keys were
// written. An absent field and a field carrying the zero value are the same map.
func toolStream(t *testing.T, box *aguix.Toolbox[deps], args string) string {
	t.Helper()
	agent := aguix.Scripted(aguix.Rule{
		Steps: []aguix.Step{aguix.CallTool(box, "borrow_book", json.RawMessage(args))},
	})
	return run(t, agent, oneTurn,
		aguix.WithOnError(func(_ *http.Request, _ error) {})).Body.String()
}

// The protocol's order, which a client depends on: a call is announced, its
// arguments follow, the argument stream closes, and only then the result.
func TestAToolCallStreamsInProtocolOrder(t *testing.T) {
	events := runTool(t, toolboxFor(t, signedIn), `{"book_id":4}`)

	got := types(events)
	want := []string{
		"RUN_STARTED", "TOOL_CALL_START", "TOOL_CALL_ARGS",
		"TOOL_CALL_END", "TOOL_CALL_RESULT", "RUN_FINISHED",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}

	// The result carries what the service returned, and the principal reached
	// Deps on the way.
	var result borrowOut
	if err := json.Unmarshal([]byte(fmt.Sprint(events[4]["content"])), &result); err != nil {
		t.Fatalf("result content is not the service's value: %v", err)
	}
	if result.LoanID != 40 || result.By != "ada" {
		t.Errorf("result = %+v", result)
	}

	// The call and its result are different ids, because the protocol needs
	// both: one identifies the call, the other the message the result becomes.
	if events[1]["toolCallId"] == events[4]["messageId"] {
		t.Error("the tool call id and the result message id are the same")
	}
}

// A refused call is a result, not a failed run: the agent asked for something
// it could not have, and the conversation continues.
func TestARefusedCallIsAResultNotAFailedRun(t *testing.T) {
	for _, tc := range []struct {
		name, args, says string
	}{
		{"permission", `{"book_id":13}`, "book 13 is reference only"},
		{"not found", `{"book_id":99}`, "no book 99"},
		{"validation", `{"book_id":0}`, "must be a positive identifier"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := runTool(t, toolboxFor(t, signedIn), tc.args)

			if last := events[len(events)-1]; last["type"] != "RUN_FINISHED" {
				t.Errorf("run ended with %v, want RUN_FINISHED", last["type"])
			}
			result := fmt.Sprint(events[len(events)-2]["content"])
			if !strings.Contains(result, tc.says) {
				t.Errorf("result = %q, want it to say %q", result, tc.says)
			}
			if strings.Contains(result, "services:") {
				t.Errorf("result = %q names the implementation's package", result)
			}
		})
	}
}

// An unexpected error inside a service is redacted here too. A tool result is
// rendered in the chat and read back by whatever decides the next move.
func TestAnUnexpectedFailureInATooIsRedacted(t *testing.T) {
	events := runTool(t, toolboxFor(t, signedIn), `{"book_id":7}`)

	result := fmt.Sprint(events[len(events)-2]["content"])
	if result != aguix.ToolResultError {
		t.Errorf("result = %q, want the fixed sentence", result)
	}
	for _, event := range events {
		if strings.Contains(fmt.Sprint(event), operatorSecret) {
			t.Fatalf("the operator's words reached the client: %v", event)
		}
	}
}

// The principal refuses before anything is dispatched, and that too is a result.
func TestAnUnauthenticatedRunGetsARefusalResult(t *testing.T) {
	events := runTool(t, toolboxFor(t, aguix.Anonymous), `{"book_id":4}`)

	result := fmt.Sprint(events[len(events)-2]["content"])
	if !strings.Contains(result, "not signed in") {
		t.Errorf("result = %q, want the resolver's own words", result)
	}
}

// The definitions carry the schema the kernel reflected, which is what makes
// what an agent is told and what the kernel enforces the same thing.
func TestDefinitionsCarryTheKernelsSchema(t *testing.T) {
	defs, err := toolboxFor(t, signedIn).Definitions()
	if err != nil {
		t.Fatalf("Definitions: %v", err)
	}
	if len(defs) != 1 {
		t.Fatalf("published %d tools, want one per spec", len(defs))
	}
	if defs[0].Name != "borrow_book" || defs[0].Description == "" {
		t.Errorf("definition = %+v", defs[0])
	}
	if !strings.Contains(string(defs[0].Parameters), "book_id") {
		t.Errorf("parameters = %s, want the reflected schema", defs[0].Parameters)
	}
}

func TestToolboxRefusesAMissingRegistryOrPrincipal(t *testing.T) {
	if _, err := aguix.NewToolbox[deps](nil, signedIn); err == nil {
		t.Error("NewToolbox accepted a nil registry")
	}
	if _, err := aguix.NewToolbox(newRegistry(t), nil); err == nil {
		t.Error("NewToolbox accepted a nil principal")
	}
}

// A call with no arguments sends an empty object rather than an empty string:
// the field is a string the client reassembles and parses.
func TestACallWithNoArgumentsSendsAnEmptyObject(t *testing.T) {
	events := runTool(t, toolboxFor(t, signedIn), ``)
	if got := events[2]["delta"]; got != "{}" {
		t.Errorf("args delta = %v, want {}", got)
	}
}

// The distinction as the content makes it.
//
// TOOL_CALL_RESULT carries a content string, and the web component settles
// every result it receives as done -- so without a marker in the content
// itself, a refusal and a success reach the model as the same event with
// different words inside. A success is bare JSON; every failure is prefixed.
// The outcome field says the same thing structurally, for a client that reads
// it; the test below holds the two together.
func TestAFailedCallIsTellableApartFromASuccess(t *testing.T) {
	box := toolboxFor(t, signedIn)

	success := fmt.Sprint(runTool(t, box, `{"book_id":4}`)[4]["content"])
	if strings.HasPrefix(success, aguix.ToolErrorPrefix) {
		t.Errorf("a success is marked as an error: %q", success)
	}
	if !strings.HasPrefix(success, "{") {
		t.Errorf("a success is not the service's own value: %q", success)
	}

	for _, tc := range []struct{ name, args string }{
		{"permission", `{"book_id":13}`},
		{"not found", `{"book_id":99}`},
		{"validation", `{"book_id":0}`},
		{"unexpected", `{"book_id":7}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := runTool(t, box, tc.args)
			got := fmt.Sprint(events[len(events)-2]["content"])
			if !strings.HasPrefix(got, aguix.ToolErrorPrefix) {
				t.Errorf("result = %q, want it marked as a failure", got)
			}
		})
	}
}

// A refusal that is not signed in is a failure too, and marked as one.
func TestAPrincipalRefusalIsMarked(t *testing.T) {
	events := runTool(t, toolboxFor(t, aguix.Anonymous), `{"book_id":4}`)
	got := fmt.Sprint(events[len(events)-2]["content"])
	if !strings.HasPrefix(got, aguix.ToolErrorPrefix) {
		t.Errorf("result = %q, want it marked as a failure", got)
	}
}

// The two markers, and the rule that they agree.
//
// A failure is marked twice: the prefix in the content, for the model and for
// every client that has not adopted the field, and the outcome on the event,
// for the ones that have. Two markers set from one error can still drift apart
// once someone adds a third failure path, so this is the invariant rather than
// a second copy of the table above -- prefixed exactly when the outcome is set,
// and set to the member of the vocabulary the taxonomy calls for.
func TestTheOutcomeAndThePrefixNeverDisagree(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		principal  aguix.Principal
		want       aguix.ToolOutcome
	}{
		{name: "success", args: `{"book_id":4}`, want: aguix.OutcomeSuccess},
		{name: "permission", args: `{"book_id":13}`, want: aguix.OutcomeDenied},
		{
			name: "principal", args: `{"book_id":4}`,
			principal: aguix.Anonymous, want: aguix.OutcomeDenied,
		},
		{name: "conflict", args: `{"book_id":5}`, want: aguix.OutcomeFailed},
		{name: "not found", args: `{"book_id":99}`, want: aguix.OutcomeFailed},
		{name: "validation", args: `{"book_id":0}`, want: aguix.OutcomeFailed},
		{name: "unexpected", args: `{"book_id":7}`, want: aguix.OutcomeFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			principal := tc.principal
			if principal == nil {
				principal = signedIn
			}
			events := runTool(t, toolboxFor(t, principal), tc.args)
			result := events[len(events)-2]

			got, marked := result["outcome"]
			if !marked {
				got = ""
			}
			if aguix.ToolOutcome(fmt.Sprint(got)) != tc.want {
				t.Errorf("outcome = %v, want %q", got, tc.want)
			}

			prefixed := strings.HasPrefix(fmt.Sprint(result["content"]), aguix.ToolErrorPrefix)
			if prefixed != marked {
				t.Errorf("content prefixed = %v but outcome present = %v: %v",
					prefixed, marked, result)
			}
		})
	}
}
