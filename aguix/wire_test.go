package aguix_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Artui/go-services/aguix"
)

// The frames, written out.
//
// This package declares the AG-UI event types itself rather than depending on
// the community Go SDK, so the one thing it can get wrong is the wire. Testing
// the structs would only prove they agree with themselves. These assert the
// bytes, against field names read out of the @ag-ui/core package the web
// component actually resolves.
func TestEventFrames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event aguix.Event
		want  string
	}{
		{
			"run started", aguix.RunStarted("t1", "r1"),
			`{"type":"RUN_STARTED","threadId":"t1","runId":"r1"}`,
		},
		{
			"run finished", aguix.RunFinished("t1", "r1"),
			`{"type":"RUN_FINISHED","threadId":"t1","runId":"r1"}`,
		},
		{
			"run error", aguix.RunError("something went wrong"),
			`{"type":"RUN_ERROR","message":"something went wrong"}`,
		},
		{
			// role defaults to assistant in the schema, and is sent explicitly:
			// a client that reads it finds what it expects, and one that does
			// not is unaffected.
			"text start", aguix.TextMessageStart("m1"),
			`{"type":"TEXT_MESSAGE_START","messageId":"m1","role":"assistant"}`,
		},
		{
			"text content", aguix.TextMessageContent("m1", "hello"),
			`{"type":"TEXT_MESSAGE_CONTENT","messageId":"m1","delta":"hello"}`,
		},
		{
			"text end", aguix.TextMessageEnd("m1"),
			`{"type":"TEXT_MESSAGE_END","messageId":"m1"}`,
		},
		{
			"tool call start", aguix.ToolCallStart("c1", "borrow_book", "m1"),
			`{"type":"TOOL_CALL_START","toolCallId":"c1",` +
				`"toolCallName":"borrow_book","parentMessageId":"m1"}`,
		},
		{
			"tool call args", aguix.ToolCallArgs("c1", json.RawMessage(`{"book_id":4}`)),
			`{"type":"TOOL_CALL_ARGS","delta":"{\"book_id\":4}","toolCallId":"c1"}`,
		},
		{
			"tool call end", aguix.ToolCallEnd("c1"),
			`{"type":"TOOL_CALL_END","toolCallId":"c1"}`,
		},
		{
			"tool call result", aguix.ToolCallResult("m2", "c1", `{"loan_id":1}`),
			`{"type":"TOOL_CALL_RESULT","messageId":"m2","role":"tool","toolCallId":"c1",` +
				`"content":"{\"loan_id\":1}"}`,
		},
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

// Every event carries its discriminator, and no event carries a field it did
// not set. The second half is what omitempty buys: a TEXT_MESSAGE_END with an
// empty "delta" would be a client rendering an empty token.
func TestNoEventCarriesEmptyFields(t *testing.T) {
	events := []aguix.Event{
		aguix.RunStarted("t", "r"), aguix.RunFinished("t", "r"), aguix.RunError("x"),
		aguix.TextMessageStart("m"), aguix.TextMessageContent("m", "d"), aguix.TextMessageEnd("m"),
		aguix.ToolCallStart("c", "n", ""), aguix.ToolCallEnd("c"),
	}
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if fields["type"] == nil {
			t.Errorf("%s carries no type", raw)
		}
		for name, value := range fields {
			if value == "" {
				t.Errorf("%s: %q is present and empty", raw, name)
			}
		}
	}
}

// A tool call with no parent message omits the field rather than sending null.
// The schema accepts null and the client normalises it away, so sending it
// would be relying on a tolerance rather than on the contract.
func TestAToolCallWithNoParentOmitsIt(t *testing.T) {
	raw, err := json.Marshal(aguix.ToolCallStart("c1", "ping", ""))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "parentMessageId") {
		t.Errorf("frame = %s, want no parentMessageId", raw)
	}
}
