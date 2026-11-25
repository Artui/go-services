package aguix_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Artui/go-services/aguix"
)

// A delegation as aguiadk will write it: the parent's call, the sub-agent's
// attributed work inside it, and each way an invocation can end.
func delegating(_ context.Context, _ aguix.RunInput, out *aguix.Stream) error {
	for _, event := range []aguix.Event{
		aguix.TextMessageStart("m1"),
		aguix.TextMessageEnd("m1"),
		aguix.ToolCallStart("c1", "research", "m1"),
		aguix.ToolCallArgs("c1", json.RawMessage(`{"topic":"tides"}`)),
		aguix.ToolCallEnd("c1"),
		aguix.SubagentStarted("s1", "researcher", "c1").WithDescription("Looks things up."),
		aguix.TextMessageStart("m2").WithSubagentRunID("s1"),
		aguix.TextMessageContent("m2", "Searching.").WithSubagentRunID("s1"),
		aguix.TextMessageEnd("m2").WithSubagentRunID("s1"),
		aguix.ToolCallStart("c2", "search", "m2").WithSubagentRunID("s1"),
		aguix.ToolCallArgs("c2", json.RawMessage(`{"q":"tides"}`)).WithSubagentRunID("s1"),
		aguix.ToolCallEnd("c2").WithSubagentRunID("s1"),
		aguix.ToolCallResult("m3", "c2", "two a day", aguix.OutcomeSuccess).WithSubagentRunID("s1"),
		aguix.SubagentStarted("s2", "checker", "").WithParentSubagentRunID("s1"),
		aguix.SubagentError("s2", "The checker could not finish.").WithCode("error"),
		aguix.SubagentFinished("s1").WithResult(map[string]any{"answer": "two a day"}),
		aguix.ToolCallResult("m4", "c1", "two a day", aguix.OutcomeSuccess),
		aguix.SubagentStarted("s3", "approver", "c1"),
		aguix.SubagentSuspended("s3", "i1"),
		aguix.SubagentStarted("s4", "noop", ""),
		aguix.SubagentFinished("s4"),
	} {
		if err := out.Emit(event); err != nil {
			return err
		}
	}
	return nil
}

// verify-transcripts.mjs reads this run through @ag-ui/core's EventSchemas and
// the client's verifier, which is the acceptance for every SUBAGENT_* frame.
func TestSubagentFramesAreRecordedForTheClient(t *testing.T) {
	golden(t, "subagents", run(t, aguix.AgentFunc(delegating), oneTurn).Body.String())
}

func TestSubagentFramesCarryTheKeysTheWebComponentReads(t *testing.T) {
	all := frames(t, run(t, aguix.AgentFunc(delegating), oneTurn).Body.String())
	byIndex := func(i int) map[string]any { return all[i+1] }
	for _, tc := range []struct {
		name string
		at   int
		want map[string]any
	}{
		{"started under a call", 5, map[string]any{
			"type": "SUBAGENT_STARTED", "subagentRunId": "s1", "name": "researcher",
			"parentToolCallId": "c1", "description": "Looks things up.",
		}},
		{"attributed work", 9, map[string]any{
			"type": "TOOL_CALL_START", "toolCallId": "c2", "toolCallName": "search",
			"parentMessageId": "m2", "subagentRunId": "s1",
		}},
		{"nested, no call", 13, map[string]any{
			"type": "SUBAGENT_STARTED", "subagentRunId": "s2", "name": "checker",
			"parentSubagentRunId": "s1",
		}},
		{"failed", 14, map[string]any{
			"type": "SUBAGENT_ERROR", "subagentRunId": "s2",
			"message": "The checker could not finish.", "code": "error",
		}},
		{"finished with a result", 15, map[string]any{
			"type": "SUBAGENT_FINISHED", "subagentRunId": "s1",
			"result": map[string]any{"answer": "two a day"},
		}},
		{"suspended", 18, map[string]any{
			"type": "SUBAGENT_FINISHED", "subagentRunId": "s3",
			"outcome": map[string]any{"type": "suspended", "interruptIds": []any{"i1"}},
		}},
		{"finished bare", 20, map[string]any{"type": "SUBAGENT_FINISHED", "subagentRunId": "s4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := byIndex(tc.at); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("frame = %v, want %v", got, tc.want)
			}
		})
	}
}

// The new fields are declared after every older one and all omit when empty,
// so a frame that sets none of them encodes as it did before they existed.
func TestFramesWithoutSubagentFieldsAreUnchanged(t *testing.T) {
	for _, tc := range []struct {
		event aguix.Event
		want  string
	}{
		{aguix.ToolCallStart("c1", "borrow_book", "m1"),
			`{"type":"TOOL_CALL_START","toolCallId":"c1","toolCallName":"borrow_book","parentMessageId":"m1"}`},
		{aguix.Custom("ping", nil), `{"type":"CUSTOM","name":"ping","value":null}`},
		{aguix.RunError("no").WithCode("error"), `{"type":"RUN_ERROR","message":"no","code":"error"}`},
		{aguix.SubagentSuspended("s1"),
			`{"type":"SUBAGENT_FINISHED","subagentRunId":"s1","outcome":{"type":"suspended"}}`},
	} {
		got, err := json.Marshal(tc.event)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Errorf("encoded %s, want %s", got, tc.want)
		}
	}
}

// A result whose encoding fails is reported by Emit rather than written as a
// SUBAGENT_FINISHED missing the key.
func TestAResultThatCannotBeEncodedIsAnError(t *testing.T) {
	if _, err := json.Marshal(aguix.SubagentFinished("s1").WithResult(make(chan int))); err == nil {
		t.Error("encoding a channel result succeeded")
	}
}
