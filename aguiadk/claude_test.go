package aguiadk

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/model/anthropicmodel"
)

// claudeAPI serves Messages API streams in order, as Claude sends them, and
// keeps each request body so a test can read what the model was sent.
type claudeAPI struct {
	mu       sync.Mutex
	streams  [][]string
	requests []map[string]any
}

func (c *claudeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	c.mu.Lock()
	n := len(c.requests)
	c.requests = append(c.requests, body)
	c.mu.Unlock()
	if stream, _ := body["stream"].(bool); !stream || n >= len(c.streams) {
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"unscripted"}}`,
			http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, ev := range c.streams[n] {
		var probe struct{ Type string }
		_ = json.Unmarshal([]byte(ev), &probe)
		_, _ = io.WriteString(w, "event: "+probe.Type+"\ndata: "+ev+"\n\n")
	}
}

func (c *claudeAPI) request(t *testing.T, i int) map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if i >= len(c.requests) {
		t.Fatalf("Claude was sent %d requests, want at least %d", len(c.requests), i+1)
	}
	return c.requests[i]
}

func claudeMessage(stopReason string, blocks ...[]string) []string {
	out := []string{`{"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant",` +
		`"model":"claude-sonnet-4-5-20250929","content":[],"usage":{"input_tokens":40,"output_tokens":1}}}`}
	for _, b := range blocks {
		out = append(out, b...)
	}
	return append(out,
		`{"type":"message_delta","delta":{"stop_reason":"`+stopReason+`","stop_sequence":null},`+
			`"usage":{"output_tokens":60}}`,
		`{"type":"message_stop"}`)
}

func claudeBlock(index, start string, deltas ...string) []string {
	out := []string{`{"type":"content_block_start","index":` + index + `,"content_block":` + start + `}`}
	for _, d := range deltas {
		out = append(out, `{"type":"content_block_delta","index":`+index+`,"delta":`+d+`}`)
	}
	return append(out, `{"type":"content_block_stop","index":`+index+`}`)
}

// Two signed thinking blocks and a redacted one, then text and a call: the
// shape of a Claude response that thought twice before using a browser tool.
var claudeThoughtTwice = claudeMessage("tool_use",
	claudeBlock("0", `{"type":"thinking","thinking":"","signature":""}`,
		`{"type":"thinking_delta","thinking":"The email field "}`,
		`{"type":"thinking_delta","thinking":"is the one to fill."}`,
		`{"type":"signature_delta","signature":"EqQBCkYIBxgCKkDfirst=="}`),
	claudeBlock("1", `{"type":"thinking","thinking":"","signature":""}`,
		`{"type":"thinking_delta","thinking":" Its value is "}`,
		`{"type":"thinking_delta","thinking":"the account address."}`,
		`{"type":"signature_delta","signature":"EpYBCkYIBxgCKkDsecond=="}`),
	claudeBlock("2", `{"type":"redacted_thinking","data":"EmwKAhgBEgy3redacted=="}`),
	claudeBlock("3", `{"type":"text","text":""}`, `{"type":"text_delta","text":"Filling it in."}`),
	claudeBlock("4", `{"type":"tool_use","id":"toolu_01","name":"fill_field","input":{}}`,
		`{"type":"input_json_delta","partial_json":"{\"field\":\"email\"}"}`),
)

var claudeDone = claudeMessage("end_turn",
	claudeBlock("0", `{"type":"text","text":""}`, `{"type":"text_delta","text":"Done."}`))

// Claude accepts the tool result only with every thinking block of the call's
// turn returned unchanged and in order, so each one's signature must survive.
func TestEveryThinkingBlockOfAClaudeTurnReturnsOnItsToolResult(t *testing.T) {
	api := &claudeAPI{streams: [][]string{claudeThoughtTwice, claudeDone}}
	srv := httptest.NewServer(api)
	defer srv.Close()
	llm, err := anthropicmodel.NewModel(t.Context(), "claude-sonnet-4-5-20250929",
		anthropicmodel.ClientConfig{APIKey: "test-key", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	bridge := mustNew(t, librarian(t, llm, ClientToolset()))
	first := input("run-think", text("user", "Fill in my email."))
	first.Tools = []aguix.ToolDefinition{fillField}
	golden(t, "reasoning-blocks", serve(t, bridge, first))

	s := serve(t, bridge, inbound(t, "reasoning-blocks-tool-result"))
	if s.last()["type"] != "RUN_FINISHED" {
		t.Fatalf("the answering run failed:\n%s", s.body)
	}
	messages, _ := api.request(t, 1)["messages"].([]any)
	var turn []any
	for _, m := range messages {
		if m, _ := m.(map[string]any); m["role"] == "assistant" {
			turn, _ = m["content"].([]any)
		}
	}
	want := []any{
		map[string]any{"type": "thinking", "thinking": "The email field is the one to fill.",
			"signature": "EqQBCkYIBxgCKkDfirst=="},
		map[string]any{"type": "thinking", "thinking": " Its value is the account address.",
			"signature": "EpYBCkYIBxgCKkDsecond=="},
		map[string]any{"type": "redacted_thinking", "data": "EmwKAhgBEgy3redacted=="},
	}
	if len(turn) < len(want) || !reflect.DeepEqual(turn[:len(want)], want) {
		raw, _ := json.MarshalIndent(turn, "", "  ")
		t.Errorf("the call's turn as Claude received it:\n%s", raw)
	}
	if last, _ := turn[len(turn)-1].(map[string]any); last["type"] != "tool_use" || last["id"] != "toolu_01" {
		t.Errorf("the turn does not end in the call: %v", last)
	}
	if !strings.Contains(s.body, "Done.") {
		t.Errorf("the answering run did not stream Claude's reply:\n%s", s.body)
	}
}
