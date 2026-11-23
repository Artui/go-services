package aguix_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Artui/go-services/aguix"
)

// The bodies under testdata/agui/inbound are written by @ag-ui/client itself,
// by scripts/agui-wire, and CI regenerates them and fails on a difference. They
// are what a browser posts, so a test here is a claim about the real wire rather
// than about JSON written to agree with the structs.
func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "agui", "inbound", name+".json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return string(raw)
}

// decoded posts a fixture through the handler and returns what the agent was
// handed, failing unless the run opened and closed normally.
func decoded(t *testing.T, body string) aguix.RunInput {
	t.Helper()
	var got aguix.RunInput
	rec := run(t, aguix.AgentFunc(func(_ context.Context, in aguix.RunInput, _ *aguix.Stream) error {
		got = in
		return nil
	}), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if kinds := strings.Join(types(frames(t, rec.Body.String())), ","); kinds != "RUN_STARTED,RUN_FINISHED" {
		t.Fatalf("frames = %s", kinds)
	}
	return got
}

// Every body the client writes starts a run. Before messages were modelled on
// @ag-ui/core 1.0, the first of these was a 400: a user turn sent as parts is an
// array where a string was expected.
func TestEveryClientFixtureStartsARun(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "agui", "inbound", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		t.Run(name, func(t *testing.T) {
			rec := run(t, aguix.AgentFunc(
				func(context.Context, aguix.RunInput, *aguix.Stream) error { return nil }),
				fixture(t, name))
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAUserTurnSentAsPartsReadsAsItsText(t *testing.T) {
	in := decoded(t, fixture(t, "parts"))
	message, ok := in.LastUserMessage()
	if !ok {
		t.Fatal("no user message")
	}
	if got := message.Text(); got != "What is in this picture?" {
		t.Errorf("Text() = %q", got)
	}
	// The image is still there for an agent that wants it.
	if !strings.Contains(string(message.Content), `"image/png"`) {
		t.Errorf("content lost the image part: %s", message.Content)
	}
	if !aguix.WhenUserSays("picture")(in) {
		t.Error("WhenUserSays does not see the text of a parts message")
	}
}

// A browser tool's call and its answer arrive on different runs, and the ids
// are what pair them.
func TestABrowserToolRoundPairsEachCallWithItsResult(t *testing.T) {
	in := decoded(t, fixture(t, "tool-round"))
	if len(in.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(in.Messages))
	}
	assistant := in.Messages[1]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 2 {
		t.Fatalf("assistant = %+v", assistant)
	}
	call := assistant.ToolCalls[0]
	if call.ID != "call-1" || call.Type != "function" || call.Function.Name != "fill_field" ||
		call.Function.Arguments != `{"field":"name","value":"Ada"}` {
		t.Errorf("call = %+v", call)
	}
	results := map[string]aguix.Message{}
	for _, m := range in.Messages {
		if m.Role == "tool" {
			results[m.ToolCallID] = m
		}
	}
	for _, c := range assistant.ToolCalls {
		if _, ok := results[c.ID]; !ok {
			t.Errorf("no result pairs with %s", c.ID)
		}
	}
	if got := results["call-1"]; got.Text() != "filled" || got.Error != "" {
		t.Errorf("call-1 result = %+v", got)
	}
	if got := results["call-2"]; got.Error != "no field named email" {
		t.Errorf("call-2 error = %q", got.Error)
	}
	if len(in.Context) != 1 || in.Context[0].Description != "The page the user is on" ||
		in.Context[0].Value != "/signup" {
		t.Errorf("context = %+v", in.Context)
	}
	if len(in.Tools) != 1 || in.Tools[0].Name != "fill_field" {
		t.Errorf("tools = %+v", in.Tools)
	}
}

func TestAnActivityKeepsItsObjectContent(t *testing.T) {
	in := decoded(t, fixture(t, "activity"))
	activity := in.Messages[1]
	if activity.Role != "activity" || activity.ActivityType != "compaction" {
		t.Fatalf("activity = %+v", activity)
	}
	var content struct{ Removed, Before, After int }
	if err := json.Unmarshal(activity.Content, &content); err != nil {
		t.Fatalf("content: %v", err)
	}
	if content.Removed != 12 || content.After != 18 {
		t.Errorf("content = %+v", content)
	}
	if got := activity.Text(); got != "" {
		t.Errorf("an activity's Text() = %q, want empty", got)
	}
}

// The web component's client drops activity messages from the history it
// sends. This is a fact about the client, not a rule of the protocol, so it is
// pinned: if a client release starts sending them, the regenerated fixture
// differs and this test says why.
func TestTheClientSendsNoActivityMessages(t *testing.T) {
	in := decoded(t, fixture(t, "activity-history"))
	for _, m := range in.Messages {
		if m.Role == "activity" {
			t.Errorf("the client sent an activity message: %+v", m)
		}
	}
	if len(in.Messages) != 2 {
		t.Errorf("messages = %d, want the two user turns", len(in.Messages))
	}
}

func TestReasoningCarriesItsSignatureBack(t *testing.T) {
	in := decoded(t, fixture(t, "reasoning"))
	reasoning, assistant := in.Messages[1], in.Messages[2]
	if reasoning.Role != "reasoning" || reasoning.EncryptedValue != "sig-abc" ||
		reasoning.Text() != "Rayleigh scattering favours short wavelengths." {
		t.Errorf("reasoning = %+v", reasoning)
	}
	if string(assistant.Metadata) != `{"model":"test"}` {
		t.Errorf("metadata = %s", assistant.Metadata)
	}
	if assistant.SubagentRunID != "subagent-call-9" {
		t.Errorf("subagentRunId = %q", assistant.SubagentRunID)
	}
	if string(in.State) != `{"draft":"sunset","turns":2}` {
		t.Errorf("state = %s", in.State)
	}
}

func TestAResumeAnswersEachInterrupt(t *testing.T) {
	in := decoded(t, fixture(t, "resume"))
	if len(in.Resume) != 2 {
		t.Fatalf("resume = %+v", in.Resume)
	}
	approved, cancelled := in.Resume[0], in.Resume[1]
	if approved.InterruptID != "int-call-1" || approved.Status != "resolved" {
		t.Errorf("approved = %+v", approved)
	}
	var payload struct {
		Approved   bool            `json:"approved"`
		EditedArgs json.RawMessage `json:"editedArgs"`
	}
	if err := json.Unmarshal(approved.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if !payload.Approved || string(payload.EditedArgs) != `{"id":4}` {
		t.Errorf("payload = %s", approved.Payload)
	}
	if cancelled.Status != "cancelled" || cancelled.Payload != nil {
		t.Errorf("cancelled = %+v", cancelled)
	}
}

// A role this package has never heard of is kept as sent rather than failing
// the run, so a newer client's history still decodes.
func TestAnUnknownRoleIsKept(t *testing.T) {
	in := decoded(t, `{"threadId":"t1","runId":"r1","messages":[`+
		`{"id":"x1","role":"narrator","content":{"scene":2}},`+
		`{"id":"u1","role":"user","content":"hello"}]}`)
	if in.Messages[0].Role != "narrator" || string(in.Messages[0].Content) != `{"scene":2}` {
		t.Errorf("message = %+v", in.Messages[0])
	}
}

// Text reads content the way @ag-ui/core's contentToText does, and is total: a
// Message built in Go can hold bytes no decoder produced.
func TestTextReadsEveryShapeOfContent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{"absent", ``, ""},
		{"a string", `"hello"`, "hello"},
		{"parts", `[{"type":"text","text":"a"},{"type":"image"},{"type":"text","text":"b"}]`, "ab"},
		{"a part that is not text", `[{"type":"image","text":"alt"}]`, ""},
		{"no parts", `[]`, ""},
		{"an object", `{"removed":1}`, ""},
		{"null", `null`, ""},
		{"a number", `4`, ""},
		{"a broken string", `"unterminated`, ""},
		{"parts that are not objects", `[1,2]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := aguix.Message{Content: json.RawMessage(tc.content)}
			if got := m.Text(); got != tc.want {
				t.Errorf("Text() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTextContentRoundTrips(t *testing.T) {
	m := aguix.Message{ID: "m", Role: "user", Content: aguix.TextContent(`say "hi"`)}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"id":"m","role":"user","content":"say \"hi\""}`; string(raw) != want {
		t.Errorf("encoded = %s, want %s", raw, want)
	}
	if got := m.Text(); got != `say "hi"` {
		t.Errorf("Text() = %q", got)
	}
}
