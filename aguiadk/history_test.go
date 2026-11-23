package aguiadk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/anthropicmodel"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const agentName = "concierge"

func user(text string) aguix.Message {
	return aguix.Message{Role: "user", Content: aguix.TextContent(text)}
}

func assistant(text string, calls ...aguix.ToolCall) aguix.Message {
	m := aguix.Message{Role: "assistant", ToolCalls: calls}
	if text != "" {
		m.Content = aguix.TextContent(text)
	}
	return m
}

func call(id, name, args string) aguix.ToolCall {
	return aguix.ToolCall{ID: id, Type: "function", Function: aguix.FunctionCall{Name: name, Arguments: args}}
}

func result(id, text string) aguix.Message {
	return aguix.Message{Role: "tool", ToolCallID: id, Content: aguix.TextContent(text)}
}

// render writes a content as one line, so a table can state a conversation.
func render(c *genai.Content) string {
	if c == nil {
		return "<nil>"
	}
	var parts []string
	for _, p := range c.Parts {
		switch {
		case p.FunctionCall != nil:
			args, _ := json.Marshal(p.FunctionCall.Args)
			parts = append(parts, fmt.Sprintf("call(%s,%s,%s)", p.FunctionCall.ID, p.FunctionCall.Name, args))
		case p.FunctionResponse != nil:
			resp, _ := json.Marshal(p.FunctionResponse.Response)
			parts = append(parts, fmt.Sprintf("resp(%s,%s,%s)", p.FunctionResponse.ID, p.FunctionResponse.Name, resp))
		default:
			parts = append(parts, "text("+p.Text+")")
		}
	}
	return c.Role + ":" + strings.Join(parts, " ")
}

func renderHistory(events []*session.Event) []string {
	var out []string
	for _, ev := range events {
		out = append(out, ev.Author+"|"+render(ev.Content))
	}
	return out
}

func TestRebuild(t *testing.T) {
	cases := []struct {
		name     string
		messages []aguix.Message
		history  []string
		tail     string
	}{
		{
			name:     "empty conversation",
			messages: nil,
			tail:     "<nil>",
		},
		{
			name:     "a single user message is the tail",
			messages: []aguix.Message{user("hi")},
			tail:     "user:text(hi)",
		},
		{
			name: "text parts of a user message are joined",
			messages: []aguix.Message{{Role: "user", Content: json.RawMessage(
				`[{"type":"text","text":"a"},{"type":"binary","mimeType":"image/png"},{"type":"text","text":"b"}]`)}},
			tail: "user:text(ab)",
		},
		{
			name:     "ending on an assistant message has no tail",
			messages: []aguix.Message{user("hi"), assistant("hello")},
			history:  []string{"user|user:text(hi)", agentName + "|model:text(hello)"},
			tail:     "<nil>",
		},
		{
			name: "trailing tool results merge into one tail",
			messages: []aguix.Message{
				user("weather?"),
				assistant("checking", call("toolu_1", "forecast", `{"city":"Oslo"}`), call("toolu_2", "forecast", "")),
				result("toolu_1", `{"temp":3}`),
				result("toolu_2", "sunny"),
			},
			history: []string{
				"user|user:text(weather?)",
				agentName + `|model:text(checking) call(toolu_1,forecast,{"city":"Oslo"}) call(toolu_2,forecast,{})`,
			},
			tail: `user:resp(toolu_1,forecast,{"temp":3}) resp(toolu_2,forecast,{"result":"sunny"})`,
		},
		{
			name: "tool results in history merge and the next user message is the tail",
			messages: []aguix.Message{
				user("go"),
				assistant("", call("toolu_1", "a", "{}"), call("toolu_2", "b", "{}")),
				result("toolu_1", "one"),
				{Role: "tool", ToolCallID: "toolu_2", Content: aguix.TextContent("ignored"), Error: "boom"},
				assistant("done"),
				user("thanks"),
			},
			history: []string{
				"user|user:text(go)",
				agentName + "|model:call(toolu_1,a,{}) call(toolu_2,b,{})",
				`user|user:resp(toolu_1,a,{"result":"one"}) resp(toolu_2,b,{"error":"boom"})`,
				agentName + "|model:text(done)",
			},
			tail: "user:text(thanks)",
		},
		{
			name: "consecutive assistant messages are one model turn",
			messages: []aguix.Message{
				user("go"),
				assistant("thinking aloud"),
				assistant("", call("toolu_1", "a", "{}")),
				assistant("", call("toolu_2", "b", "{}")),
				result("toolu_1", "one"),
				result("toolu_2", "two"),
			},
			history: []string{
				"user|user:text(go)",
				agentName + "|model:text(thinking aloud) call(toolu_1,a,{}) call(toolu_2,b,{})",
			},
			tail: `user:resp(toolu_1,a,{"result":"one"}) resp(toolu_2,b,{"result":"two"})`,
		},
		{
			name: "a result pairs with the latest call of its id",
			messages: []aguix.Message{
				user("go"),
				assistant("", call("dup", "first", "{}")),
				result("dup", "1"),
				assistant("", call("dup", "second", "{}")),
				result("dup", "2"),
			},
			history: []string{
				"user|user:text(go)",
				agentName + "|model:call(dup-1,first,{})",
				`user|user:resp(dup-1,first,{"result":"1"})`,
				agentName + "|model:call(dup,second,{})",
			},
			tail: `user:resp(dup,second,{"result":"2"})`,
		},
		{
			name: "system, developer, reasoning and activity messages are dropped",
			messages: []aguix.Message{
				{Role: "system", Content: aguix.TextContent("be terse")},
				{Role: "developer", Content: aguix.TextContent("debug")},
				user("hi"),
				{Role: "reasoning", Content: aguix.TextContent("hmm")},
				assistant("hello"),
				{Role: "activity", ActivityType: "plan", Content: json.RawMessage(`{"step":1}`)},
				user("again"),
				{Role: "reasoning", Content: aguix.TextContent("hmm")},
			},
			history: []string{"user|user:text(hi)", agentName + "|model:text(hello)"},
			tail:    "user:text(again)",
		},
		{
			name: "an assistant message with nothing to replay is skipped",
			messages: []aguix.Message{
				user("hi"),
				assistant(""),
				user("still there?"),
			},
			history: []string{"user|user:text(hi)"},
			tail:    "user:text(still there?)",
		},
		{
			name: "large integers survive",
			messages: []aguix.Message{
				user("go"),
				assistant("", call("toolu_1", "a", `{"n":9007199254740993}`)),
				result("toolu_1", `{"n":9007199254740995}`),
			},
			history: []string{
				"user|user:text(go)",
				agentName + `|model:call(toolu_1,a,{"n":9007199254740993})`,
			},
			tail: `user:resp(toolu_1,a,{"n":9007199254740995})`,
		},
		{
			name: "a result that is JSON but not an object is wrapped",
			messages: []aguix.Message{
				user("go"),
				assistant("", call("toolu_1", "a", "{}")),
				result("toolu_1", `[1,2]`),
			},
			history: []string{"user|user:text(go)", agentName + "|model:call(toolu_1,a,{})"},
			tail:    `user:resp(toolu_1,a,{"result":"[1,2]"})`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			history, tail, err := rebuild(tc.messages, agentName)
			if err != nil {
				t.Fatalf("rebuild: %v", err)
			}
			if got := renderHistory(history); !reflect.DeepEqual(got, tc.history) {
				t.Errorf("history:\n got %q\nwant %q", got, tc.history)
			}
			if got := render(tail); got != tc.tail {
				t.Errorf("tail:\n got %s\nwant %s", got, tc.tail)
			}
		})
	}
}

func TestRebuildRejectsInvalidArguments(t *testing.T) {
	_, _, err := rebuild([]aguix.Message{user("go"), assistant("", call("toolu_1", "a", `{"x":`))}, agentName)
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err = %v, want invalid JSON", err)
	}
}

func TestRebuildRejectsNonObjectArguments(t *testing.T) {
	for _, args := range []string{`[1]`, `"x"`, `null`} {
		_, _, err := rebuild([]aguix.Message{user("go"), assistant("", call("toolu_1", "a", args))}, agentName)
		if err == nil || !strings.Contains(err.Error(), "not a JSON object") {
			t.Errorf("args %s: err = %v, want not an object", args, err)
		}
	}
}

func TestRebuildKeepsANullToolResultAsText(t *testing.T) {
	_, tail, err := rebuild([]aguix.Message{
		user("go"), assistant("", call("toolu_1", "a", `{}`)), result("toolu_1", "null"),
	}, agentName)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := render(tail), `user:resp(toolu_1,a,{"result":"null"})`; got != want {
		t.Errorf("tail = %s, want %s", got, want)
	}
}

func TestRebuildRejectsUnknownToolCall(t *testing.T) {
	_, _, err := rebuild([]aguix.Message{user("go"), result("toolu_9", "x")}, agentName)
	if err == nil || !strings.Contains(err.Error(), `unknown tool call "toolu_9"`) {
		t.Fatalf("err = %v, want unknown tool call", err)
	}
}

// recordingLLM renders every request it receives and answers with text.
type recordingLLM struct {
	mu       sync.Mutex
	requests [][]string
}

func (m *recordingLLM) Name() string { return "recording" }

func (m *recordingLLM) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	var contents []string
	for _, c := range req.Contents {
		contents = append(contents, render(c))
	}
	m.mu.Lock()
	m.requests = append(m.requests, contents)
	m.mu.Unlock()
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: genai.NewContentFromText("noted", genai.RoleModel)}, nil)
	}
}

// runRebuilt replays messages into a fresh session and runs the tail, as the bridge will.
func runRebuilt(t *testing.T, llm model.LLM, messages []aguix.Message) []error {
	t.Helper()
	history, tail, err := rebuild(messages, agentName)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	a, err := llmagent.New(llmagent.Config{Name: agentName, Model: llm, Instruction: "help"})
	if err != nil {
		t.Fatal(err)
	}
	svc := session.InMemoryService()
	r, err := runner.New(runner.Config{AppName: "app", Agent: a, SessionService: svc})
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.Create(t.Context(), &session.CreateRequest{AppName: "app", UserID: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range history {
		if err := svc.AppendEvent(t.Context(), created.Session, ev); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	var errs []error
	for _, err := range r.Run(t.Context(), "ada", created.Session.ID(), tail, agent.RunConfig{}) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// A browser-tool round: two calls made in one assistant turn of an earlier
// run, answered by two tool messages that end the conversation.
var browserRound = []aguix.Message{
	user("fill the form"),
	assistant("on it", call("toolu_01A", "fill_field", `{"field":"name","value":"Ada"}`),
		call("toolu_01B", "fill_field", `{"field":"city","value":"Oslo"}`)),
	result("toolu_01A", `{"ok":true}`),
	result("toolu_01B", "filled"),
}

// A longer conversation: a finished tool round in history, then a turn the
// client split into two assistant messages, answered by the tail.
var splitRound = []aguix.Message{
	user("what is on the page?"),
	assistant("", call("toolu_01C", "read_page", "")),
	result("toolu_01C", "a form"),
	assistant("a form"),
	user("submit it"),
	assistant("", call("toolu_01D", "validate", "{}")),
	assistant("", call("toolu_01E", "submit", "{}")),
	result("toolu_01D", "valid"),
	result("toolu_01E", "sent"),
}

// A client that reuses a call id across turns, as a model's short ids can.
var reusedID = []aguix.Message{
	user("go"),
	assistant("", call("dup", "first", "{}")),
	result("dup", "1"),
	assistant("", call("dup", "second", "{}")),
	result("dup", "2"),
}

func TestRebuiltHistoryReachesTheModelAsItsOwnTurns(t *testing.T) {
	cases := []struct {
		name     string
		messages []aguix.Message
		want     []string
	}{
		{
			name:     "browser round",
			messages: browserRound,
			want: []string{
				"user:text(fill the form)",
				`model:text(on it) call(toolu_01A,fill_field,{"field":"name","value":"Ada"}) call(toolu_01B,fill_field,{"field":"city","value":"Oslo"})`,
				`user:resp(toolu_01A,fill_field,{"ok":true}) resp(toolu_01B,fill_field,{"result":"filled"})`,
			},
		},
		{
			name:     "split assistant turn",
			messages: splitRound,
			want: []string{
				"user:text(what is on the page?)",
				"model:call(toolu_01C,read_page,{})",
				`user:resp(toolu_01C,read_page,{"result":"a form"})`,
				"model:text(a form)",
				"user:text(submit it)",
				"model:call(toolu_01D,validate,{}) call(toolu_01E,submit,{})",
				`user:resp(toolu_01D,validate,{"result":"valid"}) resp(toolu_01E,submit,{"result":"sent"})`,
			},
		},
		{
			name:     "reused id",
			messages: reusedID,
			want: []string{
				"user:text(go)",
				"model:call(dup-1,first,{})",
				`user:resp(dup-1,first,{"result":"1"})`,
				"model:call(dup,second,{})",
				`user:resp(dup,second,{"result":"2"})`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm := &recordingLLM{}
			if errs := runRebuilt(t, llm, tc.messages); len(errs) > 0 {
				t.Fatalf("run: %v", errs)
			}
			if len(llm.requests) != 1 {
				t.Fatalf("model called %d times, want 1", len(llm.requests))
			}
			if got := llm.requests[0]; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("contents the model received:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

type anthropicBlock struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	ToolUseID string `json:"tool_use_id"`
}

type anthropicRequest struct {
	Messages []struct {
		Role    string           `json:"role"`
		Content []anthropicBlock `json:"content"`
	} `json:"messages"`
}

// captureAnthropic runs messages through anthropicmodel against a server that
// records the first request body and refuses it.
func captureAnthropic(t *testing.T, messages []aguix.Message) anthropicRequest {
	t.Helper()
	var (
		mu     sync.Mutex
		bodies [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"captured"}}`)
	}))
	defer srv.Close()
	llm, err := anthropicmodel.NewModel(t.Context(), "claude-sonnet-4-5", anthropicmodel.ClientConfig{
		APIKey: "test-key", BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if errs := runRebuilt(t, llm, messages); len(errs) == 0 {
		t.Fatal("the refusing server produced no error")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) == 0 {
		t.Fatal("no request reached the server")
	}
	var req anthropicRequest
	if err := json.Unmarshal(bodies[0], &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return req
}

// assertPaired checks that each assistant message's tool_use ids are answered,
// all of them, by the very next user message.
func assertPaired(t *testing.T, req anthropicRequest, wantUses int) {
	t.Helper()
	uses := 0
	for i, m := range req.Messages {
		if m.Role != "assistant" {
			for _, b := range m.Content {
				if b.Type == "tool_result" && (i == 0 || req.Messages[i-1].Role != "assistant") {
					t.Errorf("message %d: tool_result %q does not follow an assistant message", i, b.ToolUseID)
				}
			}
			continue
		}
		var ids []string
		for _, b := range m.Content {
			if b.Type == "tool_use" {
				ids = append(ids, b.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		uses += len(ids)
		if i+1 >= len(req.Messages) || req.Messages[i+1].Role != "user" {
			t.Errorf("message %d: tool_use %q has no following user message", i, ids)
			continue
		}
		var answered []string
		for _, b := range req.Messages[i+1].Content {
			if b.Type == "tool_result" {
				answered = append(answered, b.ToolUseID)
			}
		}
		if !reflect.DeepEqual(answered, ids) {
			t.Errorf("message %d: tool_use ids %q answered by tool_result ids %q in the next message", i, ids, answered)
		}
	}
	if uses != wantUses {
		t.Errorf("tool_use blocks = %d, want %d", uses, wantUses)
	}
}

func TestRebuiltHistoryPairsOnTheAnthropicWire(t *testing.T) {
	t.Run("browser round", func(t *testing.T) {
		req := captureAnthropic(t, browserRound)
		assertPaired(t, req, 2)
		if n := len(req.Messages); n != 3 {
			t.Errorf("messages = %d, want 3 (user, assistant, user with both results)", n)
		}
		last := req.Messages[len(req.Messages)-1]
		if got := []string{last.Content[0].ToolUseID, last.Content[1].ToolUseID}; !reflect.DeepEqual(got, []string{"toolu_01A", "toolu_01B"}) {
			t.Errorf("tool_use_ids = %q, want the AG-UI ids", got)
		}
	})
	t.Run("split assistant turn", func(t *testing.T) {
		assertPaired(t, captureAnthropic(t, splitRound), 3)
	})
	t.Run("reused id", func(t *testing.T) {
		assertPaired(t, captureAnthropic(t, reusedID), 2)
	})
	t.Run("ids carrying ADK's generated prefix", func(t *testing.T) {
		req := captureAnthropic(t, []aguix.Message{
			user("go"),
			assistant("", call("adk-1", "lookup", `{"q":"a"}`), call("adk-2", "lookup", `{"q":"b"}`)),
			result("adk-1", "A"),
			result("adk-2", "B"),
		})
		assertPaired(t, req, 2)
	})
}
