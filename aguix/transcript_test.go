package aguix_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Artui/go-services/aguix"
)

// finished is what one call to the finish hook was handed.
type finished struct {
	calls    int
	ctxErr   error
	in       aguix.RunInput
	end      aguix.Event
	messages []aguix.Message
}

func (f *finished) hook(ctx context.Context, in aguix.RunInput, end aguix.Event, messages []aguix.Message) {
	f.calls++
	f.ctxErr, f.in, f.end, f.messages = ctx.Err(), in, end, messages
}

func emitting(events ...aguix.Event) aguix.Agent {
	return aguix.AgentFunc(func(_ context.Context, _ aguix.RunInput, out *aguix.Stream) error {
		for _, ev := range events {
			if err := out.Emit(ev); err != nil {
				return err
			}
		}
		return nil
	})
}

func messagesJSON(t *testing.T, messages []aguix.Message) string {
	t.Helper()
	raw, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("messages do not encode: %v", err)
	}
	return string(raw)
}

func TestTheFinishHookGetsTheRunsMessages(t *testing.T) {
	got := &finished{}
	run(t, fullRun(), declared, aguix.WithOnFinish(got.hook))

	if got.calls != 1 {
		t.Fatalf("finish hook called %d times, want 1", got.calls)
	}
	want := `[` +
		`{"id":"m1","role":"assistant","content":"Let me check.","toolCalls":[` +
		`{"id":"c1","type":"function","function":{"name":"lookup","arguments":"{\"id\":4}"}}]},` +
		`{"id":"t1","role":"tool","content":"not found","toolCallId":"c1","metadata":{"outcome":"failed"}},` +
		`{"id":"m2","role":"assistant","content":"It is gone."}]`
	if s := messagesJSON(t, got.messages); s != want {
		t.Errorf("messages =\n%s\nwant\n%s", s, want)
	}
	if got.end.Type != aguix.EventRunFinished || len(got.end.Usage) != 1 ||
		got.end.Usage[0].CacheWriteInputTokens != 2 {
		t.Errorf("end = %+v, want RUN_FINISHED with the usage", got.end)
	}
	if got.in.RunID != "r1" {
		t.Errorf("input = %+v, want the run's", got.in)
	}
}

// The hook hands back only what this run streamed; the user turn is the
// caller's, on the RunInput it is also given.
func TestTheFinishHookLeavesTheInboundMessagesToTheCaller(t *testing.T) {
	got := &finished{}
	run(t, emitting(), oneTurn, aguix.WithOnFinish(got.hook))
	if len(got.messages) != 0 {
		t.Errorf("messages = %s, want none streamed", messagesJSON(t, got.messages))
	}
	if len(got.in.Messages) != 1 || got.in.Messages[0].ID != "u1" {
		t.Errorf("input messages = %+v, want the user turn", got.in.Messages)
	}
}

func TestTheTranscriptFoldsTheWire(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []aguix.Event
		want   string
	}{
		{"a reopened id appends to its message", []aguix.Event{
			aguix.TextMessageStart("m1"), aguix.TextMessageContent("m1", "a"), aguix.TextMessageEnd("m1"),
			aguix.TextMessageStart("m1"), aguix.TextMessageContent("m1", "b"), aguix.TextMessageEnd("m1"),
		}, `[{"id":"m1","role":"assistant","content":"ab"}]`},
		{"a call with no parent is its own message", []aguix.Event{
			aguix.ToolCallStart("c1", "f", ""),
			aguix.ToolCallArgs("c1", []byte(`{"a":`)), aguix.ToolCallArgs("c1", []byte(`1}`)),
			aguix.ToolCallEnd("c1"),
			aguix.ToolCallResult("t1", "c1", "ok", aguix.OutcomeSuccess),
		}, `[{"id":"c1","role":"assistant","toolCalls":[` +
			`{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]},` +
			`{"id":"t1","role":"tool","content":"ok","toolCallId":"c1"}]`},
		{"calls sharing a parent share its message", []aguix.Event{
			aguix.ToolCallStart("c1", "f", "m1"), aguix.ToolCallEnd("c1"),
			aguix.ToolCallStart("c2", "g", "m1"), aguix.ToolCallEnd("c2"),
		}, `[{"id":"m1","role":"assistant","toolCalls":[` +
			`{"id":"c1","type":"function","function":{"name":"f","arguments":""}},` +
			`{"id":"c2","type":"function","function":{"name":"g","arguments":""}}]}]`},
		{"an empty message is dropped", []aguix.Event{
			aguix.TextMessageStart("m1"), aguix.TextMessageEnd("m1"),
		}, `null`},
		{"reasoning, state, activity and custom frames are not messages", []aguix.Event{
			aguix.ReasoningStart("r1"), aguix.ReasoningMessageStart("r1"),
			aguix.ReasoningMessageContent("r1", "hmm"), aguix.ReasoningMessageEnd("r1"), aguix.ReasoningEnd("r1"),
			aguix.StateSnapshot(map[string]any{"a": 1}),
			aguix.ActivitySnapshot("a1", "plan", map[string]any{"step": 1}),
			aguix.Custom("x", 1),
		}, `null`},
		{"a sub-agent's text is attributed and its tool frames are not the parent's", []aguix.Event{
			aguix.ToolCallStart("c1", "research", "m1"), aguix.ToolCallEnd("c1"),
			aguix.SubagentStarted("s1", "researcher", "c1"),
			aguix.TextMessageStart("m2").WithSubagentRunID("s1"),
			aguix.TextMessageContent("m2", "Searching.").WithSubagentRunID("s1"),
			aguix.TextMessageEnd("m2").WithSubagentRunID("s1"),
			aguix.ToolCallStart("c2", "search", "m2").WithSubagentRunID("s1"),
			aguix.ToolCallArgs("c2", []byte(`{}`)).WithSubagentRunID("s1"),
			aguix.ToolCallEnd("c2").WithSubagentRunID("s1"),
			aguix.ToolCallResult("t2", "c2", "hits", aguix.OutcomeSuccess).WithSubagentRunID("s1"),
			aguix.SubagentFinished("s1"),
			aguix.ToolCallResult("t1", "c1", "tides", aguix.OutcomeSuccess),
		}, `[{"id":"m1","role":"assistant","toolCalls":[` +
			`{"id":"c1","type":"function","function":{"name":"research","arguments":""}}]},` +
			`{"id":"m2","role":"assistant","content":"Searching.","subagentRunId":"s1"},` +
			`{"id":"t1","role":"tool","content":"tides","toolCallId":"c1"}]`},
		{"frames for a call never started are ignored", []aguix.Event{
			aguix.ToolCallArgs("c9", []byte(`{}`)), aguix.ToolCallEnd("c9"),
		}, `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := &finished{}
			run(t, emitting(tc.events...), oneTurn, aguix.WithOnFinish(got.hook))
			if s := messagesJSON(t, got.messages); s != tc.want {
				t.Errorf("messages =\n%s\nwant\n%s", s, tc.want)
			}
		})
	}
}

func TestTheFinishHookSeesHowTheRunEnded(t *testing.T) {
	t.Run("an error", func(t *testing.T) {
		got := &finished{}
		run(t, aguix.AgentFunc(func(_ context.Context, _ aguix.RunInput, out *aguix.Stream) error {
			_ = out.Emit(aguix.TextMessageStart("m1"))
			_ = out.Emit(aguix.TextMessageContent("m1", "partial"))
			return errors.New("model down")
		}), oneTurn, aguix.WithOnFinish(got.hook), aguix.WithOnError(func(*http.Request, error) {}))
		if got.end.Type != aguix.EventRunError {
			t.Errorf("end = %s, want RUN_ERROR", got.end.Type)
		}
		if s := messagesJSON(t, got.messages); s != `[{"id":"m1","role":"assistant","content":"partial"}]` {
			t.Errorf("messages = %s", s)
		}
	})

	// The deadline has passed, yet the hook's context is live so a store can
	// still write; the call the timeout cut off keeps the arguments it had.
	t.Run("a timeout", func(t *testing.T) {
		got := &finished{}
		run(t, waitThenFail(
			aguix.ToolCallStart("c1", "f", "m1"), aguix.ToolCallArgs("c1", []byte(`{"a":`)),
		), declared, aguix.WithRunTimeout(10*time.Millisecond),
			aguix.WithOnFinish(got.hook), aguix.WithOnError(func(*http.Request, error) {}))
		if got.end.Type != aguix.EventRunFinished || got.end.Usage == nil {
			t.Errorf("end = %+v, want the cancelled RUN_FINISHED with usage", got.end)
		}
		if got.ctxErr != nil {
			t.Errorf("hook context is done: %v", got.ctxErr)
		}
		want := `[{"id":"m1","role":"assistant","toolCalls":[` +
			`{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"a\":"}}]}]`
		if s := messagesJSON(t, got.messages); s != want {
			t.Errorf("messages = %s", s)
		}
	})

	// A client that hangs up gets no terminal frame, so end is the zero Event;
	// the hook still runs, with what was written before the hang-up.
	t.Run("a hang-up", func(t *testing.T) {
		got := &finished{}
		h, err := aguix.Handler(fullRun(), aguix.WithOnFinish(got.hook),
			aguix.WithOnError(func(*http.Request, error) {}))
		if err != nil {
			t.Fatalf("Handler: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(oneTurn)).WithContext(ctx)
		w := &hangUp{ResponseWriter: httptest.NewRecorder(), after: 3}
		cancel()
		h.ServeHTTP(w, req)
		if got.calls != 1 || !reflect.DeepEqual(got.end, aguix.Event{}) {
			t.Errorf("calls = %d, end = %+v, want one call and no terminal event", got.calls, got.end)
		}
		if got.ctxErr != nil {
			t.Errorf("hook context is done: %v", got.ctxErr)
		}
		if s := messagesJSON(t, got.messages); s != `[{"id":"m1","role":"assistant","content":"Let me "}]` {
			t.Errorf("messages = %s", s)
		}
	})
}

func TestTheFinishHookIsNotCalledBeforeTheStreamOpens(t *testing.T) {
	got := &finished{}
	run(t, emitting(), `{"threadId":""}`, aguix.WithOnFinish(got.hook),
		aguix.WithOnError(func(*http.Request, error) {}))
	if got.calls != 0 {
		t.Errorf("finish hook called for a run that never opened")
	}
}

func TestAPanickingFinishHookIsReported(t *testing.T) {
	var reported []error
	rec := run(t, fullRun(), declared,
		aguix.WithOnError(func(_ *http.Request, err error) { reported = append(reported, err) }),
		aguix.WithOnFinish(func(context.Context, aguix.RunInput, aguix.Event, []aguix.Message) {
			panic("store bug")
		}))
	if got := types(frames(t, rec.Body.String())); got[len(got)-1] != "RUN_FINISHED" {
		t.Errorf("events = %v, want the run finished", got)
	}
	if len(reported) != 1 || !strings.Contains(reported[0].Error(), "store bug") {
		t.Errorf("reported = %v, want the one panic", reported)
	}
}

// Both options at once: each sees the run, neither disturbs the other.
func TestAnObserverAndAFinishHookTogether(t *testing.T) {
	seen, got := &recorder{}, &finished{}
	rec := run(t, fullRun(), declared, aguix.WithObserver(seen.observe), aguix.WithOnFinish(got.hook))
	if seen.wire(t) != rec.Body.String() || len(got.messages) != 3 {
		t.Errorf("observed %d events and %d messages", len(seen.events), len(got.messages))
	}
}
