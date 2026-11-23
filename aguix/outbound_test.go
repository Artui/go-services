package aguix_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Artui/go-services/aguix"
)

// outboundFrames pairs each fixture, encoded by @ag-ui/core 1.0.4 through
// scripts/agui-wire/generate.mjs, with the Go frame that must equal it.
func outboundFrames() map[string]aguix.Event {
	call := aguix.ToolCall{
		ID: "c1", Type: "function",
		Function: aguix.FunctionCall{Name: "borrow_book", Arguments: `{"book_id":4}`},
	}
	return map[string]aguix.Event{
		"state-snapshot": aguix.StateSnapshot(map[string]any{"count": 1, "items": []string{"a", "b"}}),
		"state-delta": aguix.StateDelta(
			aguix.PatchOperation{Op: "replace", Path: "/count", Value: 2},
			aguix.PatchOperation{Op: "add", Path: "/items/-", Value: "c"},
			aguix.PatchOperation{Op: "remove", Path: "/items/0"},
			aguix.PatchOperation{Op: "move", From: "/items/1", Path: "/last"},
		),
		"messages-snapshot": aguix.MessagesSnapshot([]aguix.Message{
			{ID: "u1", Role: "user", Content: aguix.TextContent("Borrow book 4.")},
			{ID: "a1", Role: "assistant", ToolCalls: []aguix.ToolCall{call}},
			{ID: "t1", Role: "tool", Content: aguix.TextContent(`{"loan_id":1}`), ToolCallID: "c1"},
			{ID: "a2", Role: "assistant", Content: aguix.TextContent("Done.")},
		}),
		"reasoning-start":           aguix.ReasoningStart("r1"),
		"reasoning-message-start":   aguix.ReasoningMessageStart("r1"),
		"reasoning-message-content": aguix.ReasoningMessageContent("r1", "Checking stock."),
		"reasoning-message-end":     aguix.ReasoningMessageEnd("r1"),
		"reasoning-end":             aguix.ReasoningEnd("r1"),
		"reasoning-encrypted-value": aguix.ReasoningEncryptedValue("message", "r1", "sig-abc=="),
		"activity-snapshot": aguix.ActivitySnapshot("act1", "chart", map[string]any{
			"kind":   "bar",
			"series": []any{map[string]any{"name": "loans", "points": []int{1, 2, 3}}},
		}).WithReplace(true),
		"activity-snapshot-default": aguix.ActivitySnapshot("act1", "chart", map[string]any{"kind": "bar"}),
		"activity-delta": aguix.ActivityDelta("act1", "chart",
			aguix.PatchOperation{Op: "replace", Path: "/kind", Value: "line"}),
		"custom-object": aguix.Custom("capability.load", map[string]any{"id": "books", "n": 2}),
		"custom-scalar": aguix.Custom("ping", "pong"),
		"custom-null":   aguix.Custom("cleared", nil),
		"run-finished-success": aguix.RunFinishedWith("t1", "r1",
			aguix.Success("c1", "c2")),
		"run-finished-interrupt": aguix.RunFinishedWith("t1", "r1", aguix.Interrupted(aguix.Interrupt{
			ID: "int-1", Reason: "tool_approval", Message: "Delete the draft?", ToolCallID: "c1",
			ResponseSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"approved": map[string]any{"type": "boolean"}},
			},
			ExpiresAt: "2026-12-01T00:00:00Z",
			Metadata:  map[string]any{"risk": "high"},
		})),
		"run-finished-interrupt-minimal": aguix.RunFinishedWith("t1", "r1",
			aguix.Interrupted(aguix.Interrupt{ID: "int-1", Reason: "input_required"})),
	}
}

// Every frame is compared to what the protocol's own package produced and
// accepted, by value: key order is the producer's business, but a key the
// schema would strip, a missing required key or a different type is a failure.
func TestOutboundFramesMatchCoreEncodedFixtures(t *testing.T) {
	for name, event := range outboundFrames() {
		t.Run(name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", "agui", "outbound", name+".json"))
			if err != nil {
				t.Fatalf("fixture: %v", err)
			}
			got, err := json.Marshal(event)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var gotValue, wantValue any
			if err := json.Unmarshal(got, &gotValue); err != nil {
				t.Fatalf("frame is not JSON: %s", got)
			}
			if err := json.Unmarshal(want, &wantValue); err != nil {
				t.Fatalf("fixture is not JSON: %v", err)
			}
			if !reflect.DeepEqual(gotValue, wantValue) {
				t.Errorf("frame =\n  %s\nwant\n  %s", got, want)
			}
		})
	}
}

// A fixture nothing asserts is a frame the generator claims and this package
// never checks.
func TestEveryOutboundFixtureIsAsserted(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "agui", "outbound", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	frames := outboundFrames()
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".json")
		if _, ok := frames[name]; !ok {
			t.Errorf("fixture %s has no Go frame", name)
		}
	}
	if len(files) != len(frames) {
		t.Errorf("%d fixtures, %d frames", len(files), len(frames))
	}
}

// The payload of a snapshot, a custom event or an outcome is written under the
// key the protocol names it, with the bytes the caller gave.
func TestPayloadFramesAreByteExact(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event aguix.Event
		want  string
	}{
		{"state snapshot", aguix.StateSnapshot(map[string]any{"n": 1}),
			`{"type":"STATE_SNAPSHOT","snapshot":{"n":1}}`},
		{"state snapshot of nothing", aguix.StateSnapshot(nil),
			`{"type":"STATE_SNAPSHOT","snapshot":null}`},
		{"custom", aguix.Custom("x", []int{1}),
			`{"type":"CUSTOM","name":"x","value":[1]}`},
		{"custom raw", aguix.Custom("x", json.RawMessage(`{"big":12345678901234567890}`)),
			`{"type":"CUSTOM","name":"x","value":{"big":12345678901234567890}}`},
		{"activity snapshot", aguix.ActivitySnapshot("m", "chart", map[string]any{"a": 1}),
			`{"type":"ACTIVITY_SNAPSHOT","messageId":"m","activityType":"chart","content":{"a":1}}`},
		{"activity snapshot replace false", aguix.ActivitySnapshot("m", "chart", map[string]any{"a": 1}).WithReplace(false),
			`{"type":"ACTIVITY_SNAPSHOT","messageId":"m","activityType":"chart","replace":false,"content":{"a":1}}`},
		{"activity delta of nothing", aguix.ActivityDelta("m", "chart"),
			`{"type":"ACTIVITY_DELTA","messageId":"m","activityType":"chart","patch":[]}`},
		{"messages snapshot of nothing", aguix.MessagesSnapshot(nil),
			`{"type":"MESSAGES_SNAPSHOT","messages":[]}`},
		{"state delta", aguix.StateDelta(aguix.PatchOperation{Op: "remove", Path: "/a"}),
			`{"type":"STATE_DELTA","delta":[{"op":"remove","path":"/a"}]}`},
		{"state delta of nothing", aguix.StateDelta(),
			`{"type":"STATE_DELTA","delta":[]}`},
		{"pending tool calls", aguix.RunFinishedWith("t", "r", aguix.Success("c1")),
			`{"type":"RUN_FINISHED","threadId":"t","runId":"r","outcome":` +
				`{"type":"success","pendingToolCallIds":["c1"]}}`},
		{"success without pending", aguix.RunFinishedWith("t", "r", aguix.Success()),
			`{"type":"RUN_FINISHED","threadId":"t","runId":"r","outcome":{"type":"success"}}`},
		{"reasoning message start", aguix.ReasoningMessageStart("r1"),
			`{"type":"REASONING_MESSAGE_START","messageId":"r1","role":"reasoning"}`},
		{"plain run finished is unchanged", aguix.RunFinished("t", "r"),
			`{"type":"RUN_FINISHED","threadId":"t","runId":"r"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.event)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if got := string(raw); got != tc.want {
				t.Errorf("frame =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

// A payload that cannot be encoded is an error from Emit, not a frame with the
// key missing: the client would read a snapshot-less STATE_SNAPSHOT as invalid.
func TestAnUnencodablePayloadFailsTheMarshal(t *testing.T) {
	if _, err := json.Marshal(aguix.StateSnapshot(make(chan int))); err == nil {
		t.Error("marshal succeeded for a channel")
	}
}

// Emit hands the marshal error back and writes nothing for the frame.
func TestEmitReportsAnUnencodablePayloadAndWritesNothing(t *testing.T) {
	rec := httptest.NewRecorder()
	stream, err := aguix.NewStream(rec)
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	if err := stream.Emit(aguix.StateSnapshot(make(chan int))); err == nil {
		t.Error("Emit accepted a channel")
	}
	if body := rec.Body.String(); body != "" {
		t.Errorf("body = %q, want nothing written", body)
	}
}

func TestFinishRefusesAnUnknownOutcomeType(t *testing.T) {
	stream, err := aguix.NewStream(httptest.NewRecorder())
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	if err := stream.Finish(aguix.RunOutcome{Type: "cancelled"}); err == nil {
		t.Error("Finish accepted an unknown type")
	}
}
