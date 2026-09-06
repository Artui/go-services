package example

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Artui/go-services/aguix"
)

// The AG-UI leg, which is deliberately NOT one of the four in the mounts table.
//
// AG-UI streams an agent's turn rather than calling one spec, so there is no
// single call whose committed state could be lined up against the others'. What
// it gets instead is this: drive a run the way a browser does, then read the
// database and ask whether the run left what it should have.
//
// The script under test is the one the demo command serves, so a browser and CI
// are not looking at different agents.
func runAgent(t *testing.T, db *sql.DB, said string) ([]map[string]any, *sql.DB) {
	t.Helper()

	toolbox, err := aguix.NewToolbox(Registry(db), func(context.Context) (any, error) {
		return int64(1), nil
	})
	if err != nil {
		t.Fatalf("NewToolbox: %v", err)
	}
	handler, err := aguix.Handler(Librarian(toolbox),
		aguix.WithOnError(func(_ *http.Request, err error) { t.Logf("run failed: %v", err) }))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	body := fmt.Sprintf(
		`{"threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":%q}]}`, said)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(body)))

	var events []map[string]any
	for _, block := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n") {
		if block == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(
			[]byte(strings.TrimPrefix(block, "data: ")), &event); err != nil {
			t.Fatalf("frame is not JSON: %q", block)
		}
		events = append(events, event)
	}
	return events, db
}

func eventTypes(events []map[string]any) []string {
	var out []string
	for _, event := range events {
		out = append(out, fmt.Sprint(event["type"]))
	}
	return out
}

// outcomeOf returns the outcome the tool result reported, or "" when it carried
// none -- which is what a success looks like, and is a distinct answer from a
// failure that forgot to say so.
func outcomeOf(t *testing.T, events []map[string]any) string {
	t.Helper()
	for _, event := range events {
		if event["type"] == "TOOL_CALL_RESULT" {
			outcome, present := event["outcome"]
			if !present {
				return ""
			}
			return fmt.Sprint(outcome)
		}
	}
	t.Fatal("the run carried no tool result")
	return ""
}

func resultOf(t *testing.T, events []map[string]any) string {
	t.Helper()
	for _, event := range events {
		if event["type"] == "TOOL_CALL_RESULT" {
			return fmt.Sprint(event["content"])
		}
	}
	t.Fatal("the run carried no tool result")
	return ""
}

// A successful borrow through the agent commits both tables, exactly as it does
// through the other four transports.
func TestAnAgentBorrowCommits(t *testing.T) {
	db := newDB(t)
	events, _ := runAgent(t, db, "borrow book 10")

	want := []string{
		"RUN_STARTED", "TEXT_MESSAGE_START",
		"TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_CONTENT",
		"TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END",
		"TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT",
		"RUN_FINISHED",
	}
	if got := eventTypes(events); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("events = %v\nwant %v", got, want)
	}
	if n := countLoans(t, db); n != 1 {
		t.Errorf("loans = %d, want 1", n)
	}
	if n := availableOf(t, db, 10); n != 1 {
		t.Errorf("available = %d, want 1", n)
	}
}

// And a refused one rolls back, through a transport that has no status code to
// say so. The refusal is a tool result and the run still finishes: the agent
// asked for something it could not have, which is a turn in a conversation
// rather than a failure of the conversation.
func TestAnAgentBorrowRollsBack(t *testing.T) {
	db := newDB(t)
	events, _ := runAgent(t, db, "borrow book 11")

	if last := events[len(events)-1]; last["type"] != "RUN_FINISHED" {
		t.Errorf("run ended with %v, want RUN_FINISHED", last["type"])
	}
	result := resultOf(t, events)
	if !strings.Contains(result, "no copy of") {
		t.Errorf("result = %q, want the conflict's own words", result)
	}
	// Marked as a failure twice, and both are asserted here because they are
	// read by different consumers. The prefix is what the model is handed; the
	// outcome is what a client reads to decide how to render the call. A
	// composed run is where they can quietly come apart, since nothing inside
	// aguix alone would notice one being emitted without the other.
	if !strings.HasPrefix(result, aguix.ToolErrorPrefix) {
		t.Errorf("result = %q, want it tellable apart from a success", result)
	}
	if got := outcomeOf(t, events); got != string(aguix.OutcomeFailed) {
		t.Errorf("outcome = %q, want the conflict reported as failed", got)
	}
	if n := countLoans(t, db); n != 0 {
		t.Errorf("loans = %d, want 0: the insert escaped the transaction", n)
	}

	// The script says nothing after the call, which is what stops it claiming
	// an outcome it cannot see. A closing message here would be the demo
	// congratulating itself over a refusal.
	if strings.Contains(fmt.Sprint(events), "That is done") {
		t.Error("the script asserted an outcome after a tool call")
	}
}

// A run naming no book ends as a redacted RUN_ERROR: the words are for the log.
func TestAnAgentBorrowWithNoBookIsARedactedFailure(t *testing.T) {
	events, _ := runAgent(t, newDB(t), "borrow something")

	last := events[len(events)-1]
	if last["type"] != "RUN_ERROR" {
		t.Fatalf("run ended with %v, want RUN_ERROR", last["type"])
	}
	if last["message"] != aguix.RunErrorText {
		t.Errorf("message = %v, want the fixed sentence", last["message"])
	}
}

// Anything else gets the fallback, which tells the user what it can do.
func TestAnAgentFallsBack(t *testing.T) {
	events, _ := runAgent(t, newDB(t), "hello there")

	var said strings.Builder
	for _, event := range events {
		if event["type"] == "TEXT_MESSAGE_CONTENT" {
			fmt.Fprint(&said, event["delta"])
		}
	}
	if !strings.Contains(said.String(), "borrow book 10") {
		t.Errorf("said %q, want it to say what it can do", said.String())
	}
	for _, event := range events {
		if event["type"] == "TOOL_CALL_START" {
			t.Error("the fallback called a tool")
		}
	}
}

// The toolbox publishes the same operations the other adapters do, with the
// schema the kernel reflected.
func TestTheAgentIsOfferedEveryOperation(t *testing.T) {
	toolbox, err := aguix.NewToolbox(Registry(newDB(t)), aguix.Anonymous)
	if err != nil {
		t.Fatalf("NewToolbox: %v", err)
	}
	defs, err := toolbox.Definitions()
	if err != nil {
		t.Fatalf("Definitions: %v", err)
	}
	if len(defs) != 3 {
		t.Fatalf("published %d tools, want one per spec", len(defs))
	}
	for _, def := range defs {
		if def.Description == "" {
			t.Errorf("%q has no description", def.Name)
		}
		if !strings.Contains(string(def.Parameters), "properties") {
			t.Errorf("%q carries no reflected schema: %s", def.Name, def.Parameters)
		}
	}
}

// A success carries no outcome key at all, which is the half of the contract
// that keeps every client that has not adopted the field correct.
//
// Asserted from the composed stack rather than only from aguix's own frame
// tests: this is the run a browser makes, and an absence is the kind of thing
// that survives a unit test and gets written by accident somewhere upstream.
func TestASuccessfulAgentCallReportsNoOutcome(t *testing.T) {
	events, _ := runAgent(t, newDB(t), "borrow book 10")

	if got := outcomeOf(t, events); got != "" {
		t.Errorf("outcome = %q on a success, want no key at all", got)
	}
	if result := resultOf(t, events); strings.HasPrefix(result, aguix.ToolErrorPrefix) {
		t.Errorf("result = %q, want a success unmarked", result)
	}
}
