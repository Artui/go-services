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
			// A success is the frame this event was before the outcome field
			// existed. That is the compatibility claim, asserted in bytes.
			"tool call result", aguix.ToolCallResult("m2", "c1", `{"loan_id":1}`,
				aguix.OutcomeSuccess),
			`{"type":"TOOL_CALL_RESULT","messageId":"m2","role":"tool","toolCallId":"c1",` +
				`"content":"{\"loan_id\":1}"}`,
		},
		{
			"tool call result, failed", aguix.ToolCallResult("m2", "c1",
				"Error: not found: no book 99", aguix.OutcomeFailed),
			`{"type":"TOOL_CALL_RESULT","messageId":"m2","role":"tool","toolCallId":"c1",` +
				`"content":"Error: not found: no book 99","outcome":"failed"}`,
		},
		{
			"tool call result, denied", aguix.ToolCallResult("m2", "c1",
				"Error: permission denied: this run is not signed in", aguix.OutcomeDenied),
			`{"type":"TOOL_CALL_RESULT","messageId":"m2","role":"tool","toolCallId":"c1",` +
				`"content":"Error: permission denied: this run is not signed in",` +
				`"outcome":"denied"}`,
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

// resultFrame returns the TOOL_CALL_RESULT frame from a stream, exactly as it
// was written.
func resultFrame(t *testing.T, body string) string {
	t.Helper()
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		frame := strings.TrimPrefix(block, "data: ")
		if strings.Contains(frame, `"TOOL_CALL_RESULT"`) {
			return frame
		}
	}
	t.Fatalf("no TOOL_CALL_RESULT frame in %q", body)
	return ""
}

// The result frame a client actually receives, per kind of ending.
//
// The table above builds events through the constructors; this drives the whole
// emit path, because how a call ended is decided in toolbox.go and a
// constructor cannot be asked about that. The bytes are written out in full --
// including, for the success, the fact that the frame ends after "content".
func TestAToolResultFrameSaysHowTheCallEnded(t *testing.T) {
	const head = `{"type":"TOOL_CALL_RESULT","messageId":"call-2","role":"tool",` +
		`"toolCallId":"call-1","content":`

	for _, tc := range []struct {
		name      string
		principal aguix.Principal
		args      string
		want      string
	}{
		{
			// Absent, not "success". A producer that has not adopted the field
			// is indistinguishable from one reporting a success, so absence is
			// what the contract had to mean, and emitting the word as well
			// would invite a client to require a key half its servers omit.
			name: "a success carries no outcome at all",
			args: `{"book_id":4}`,
			want: head + `"{\"loan_id\":40,\"by\":\"ada\"}"}`,
		},
		{
			name: "a permission refusal is denied",
			args: `{"book_id":13}`,
			want: head + `"Error: permission denied: book 13 is reference only",` +
				`"outcome":"denied"}`,
		},
		{
			// The principal refuses before the registry is reached at all, and
			// it is the same denial: the acting principal may not do this.
			name:      "an unauthenticated run is denied",
			principal: aguix.Anonymous,
			args:      `{"book_id":4}`,
			want: head + `"Error: permission denied: this run is not signed in",` +
				`"outcome":"denied"}`,
		},
		{
			name: "a conflict failed",
			args: `{"book_id":5}`,
			want: head + `"Error: conflict: no copy of book 5 is on the shelf",` +
				`"outcome":"failed"}`,
		},
		{
			name: "a missing row failed",
			args: `{"book_id":99}`,
			want: head + `"Error: not found: no book 99","outcome":"failed"}`,
		},
		{
			name: "rejected arguments failed",
			args: `{"book_id":0}`,
			want: head + `"Error: The arguments were rejected. Correct these and try again:` +
				`\n- book_id: must be a positive identifier","outcome":"failed"}`,
		},
		{
			name: "an error outside the taxonomy failed",
			args: `{"book_id":7}`,
			want: head + `"Error: The operation failed. The reason was recorded on the ` +
				`server.","outcome":"failed"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			principal := tc.principal
			if principal == nil {
				principal = signedIn
			}
			got := resultFrame(t, toolStream(t, toolboxFor(t, principal), tc.args))
			if got != tc.want {
				t.Errorf("frame =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

// The success case again, on its own and stated as the absence of a key rather
// than as a byte string.
//
// A frame comparison would also fail if the field were emitted as an empty
// string, or as null, or last but spelled differently -- and would not say
// which. This one says it: the key is not there.
func TestASuccessfulResultHasNoOutcomeKey(t *testing.T) {
	var fields map[string]any
	frame := resultFrame(t, toolStream(t, toolboxFor(t, signedIn), `{"book_id":4}`))
	if err := json.Unmarshal([]byte(frame), &fields); err != nil {
		t.Fatalf("frame is not JSON: %v", err)
	}
	if value, ok := fields["outcome"]; ok {
		t.Errorf("a successful result carries outcome=%v, want no such key", value)
	}
}
