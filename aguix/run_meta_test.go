package aguix_test

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/aguix"
)

var update = flag.Bool("update", false, "rewrite the SSE transcripts under testdata")

const declared = `{"threadId":"t1","runId":"r1","protocolVersion":"1.0","messages":[]}`

// golden compares a run's body with its transcript, which scripts/agui-wire's
// verify-transcripts.mjs reads through the client's own schemas and verifier.
func golden(t *testing.T, name, body string) {
	t.Helper()
	path := filepath.Join("testdata", "agui", "transcripts", name+".sse")
	got := []byte(strings.TrimSuffix(body, "\n"))
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the tests with -update to write it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s drifted from the transcript:\ngot:\n%s\nwant:\n%s", name, got, want)
	}
}

type timeoutError struct{ timeout bool }

func (e timeoutError) Error() string { return "i/o timeout" }
func (e timeoutError) Timeout() bool { return e.timeout }

func failing(err error) aguix.Agent {
	return aguix.AgentFunc(func(context.Context, aguix.RunInput, *aguix.Stream) error { return err })
}

func TestARunErrorSaysWhatKindOfFailureItWas(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		code    string
		message string
	}{
		{"an unexpected failure", errors.New("dial tcp: refused"), aguix.CodeError, aguix.RunErrorText},
		{"a refusal keeps its words and is an error",
			fmt.Errorf("%w: read only", services.ErrPermission), aguix.CodeError, "permission denied: read only"},
		{"the agent's own deadline", fmt.Errorf("model call: %w", context.DeadlineExceeded),
			aguix.CodeTimedOut, aguix.RunErrorText},
		{"an error reporting a timeout", fmt.Errorf("read: %w", timeoutError{true}),
			aguix.CodeTimedOut, aguix.RunErrorText},
		{"an error saying it is no timeout", timeoutError{false}, aguix.CodeError, aguix.RunErrorText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := lastFrame(t, run(t, failing(tc.err), oneTurn))
			want := map[string]any{"type": "RUN_ERROR", "message": tc.message, "code": tc.code}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("last frame = %v, want %v", got, want)
			}
		})
	}
}

// The handler's frame, not only the constructor's, is the fixture the client wrote.
func TestTheHandlersRunErrorMatchesTheFixture(t *testing.T) {
	rec := run(t, failing(context.DeadlineExceeded), oneTurn)
	all := frames(t, rec.Body.String())
	got := all[len(all)-1]
	got["message"] = "The assistant could not complete that."
	want := outboundFixture(t, "run-error-code")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("frame = %v, want %v", got, want)
	}
}

func outboundFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "agui", "outbound", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return frames(t, "data: "+string(raw))[0]
}

var someUsage = []aguix.TokenUsage{
	{
		Provider: "anthropic", Model: "claude-opus-5-5", InputTokens: 1200, OutputTokens: 340,
		TotalTokens: 1540, ReasoningTokens: 120, CachedInputTokens: 800, CacheWriteInputTokens: 64,
	},
	{Provider: "google", Model: "gemini-flash", InputTokens: 9},
}

func TestUsageTheAgentSetsRidesOnRunFinished(t *testing.T) {
	agent := aguix.AgentFunc(func(_ context.Context, _ aguix.RunInput, out *aguix.Stream) error {
		return out.SetUsage(someUsage...)
	})
	got := lastFrame(t, run(t, agent, `{"threadId":"t1","runId":"r1","messages":[]}`))
	if want := outboundFixture(t, "run-finished-usage"); !reflect.DeepEqual(got, want) {
		t.Errorf("last frame = %v, want %v", got, want)
	}
}

func TestUsageRidesBesideAnOutcome(t *testing.T) {
	agent := aguix.AgentFunc(func(_ context.Context, _ aguix.RunInput, out *aguix.Stream) error {
		if err := out.SetUsage(aguix.TokenUsage{OutputTokens: 3}); err != nil {
			return err
		}
		return out.Finish(aguix.Success("c1"))
	})
	got := lastFrame(t, run(t, agent, oneTurn))
	want := map[string]any{
		"type": "RUN_FINISHED", "threadId": "t1", "runId": "r1",
		"outcome": map[string]any{"type": "success", "pendingToolCallIds": []any{"c1"}},
		"usage":   []any{map[string]any{"outputTokens": float64(3)}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("last frame = %v, want %v", got, want)
	}
}

func TestUsageIsAbsentUnlessSet(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage []aguix.TokenUsage
	}{
		{"never set", nil},
		{"set to nothing", []aguix.TokenUsage{}},
		{"only entries with neither input nor output",
			[]aguix.TokenUsage{{Provider: "p", Model: "m", TotalTokens: 4, ReasoningTokens: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := aguix.AgentFunc(func(_ context.Context, _ aguix.RunInput, out *aguix.Stream) error {
				if tc.usage == nil {
					return nil
				}
				return out.SetUsage(tc.usage...)
			})
			rec := run(t, agent, oneTurn)
			if strings.Contains(rec.Body.String(), `"usage"`) {
				t.Errorf("body = %s, want no usage", rec.Body.String())
			}
		})
	}
}

func TestUsageIsNeverOnARunError(t *testing.T) {
	agent := aguix.AgentFunc(func(_ context.Context, _ aguix.RunInput, out *aguix.Stream) error {
		if err := out.SetUsage(someUsage...); err != nil {
			return err
		}
		return errors.New("after the model was paid for")
	})
	got := lastFrame(t, run(t, agent, oneTurn))
	if _, ok := got["usage"]; ok || got["type"] != "RUN_ERROR" {
		t.Errorf("last frame = %v, want a RUN_ERROR with no usage", got)
	}
}

func TestTheLastUsageWins(t *testing.T) {
	agent := aguix.AgentFunc(func(_ context.Context, _ aguix.RunInput, out *aguix.Stream) error {
		if err := out.SetUsage(aguix.TokenUsage{InputTokens: 1}); err != nil {
			return err
		}
		return out.SetUsage(aguix.TokenUsage{InputTokens: 2})
	})
	got := lastFrame(t, run(t, agent, oneTurn))
	if want := []any{map[string]any{"inputTokens": float64(2)}}; !reflect.DeepEqual(got["usage"], want) {
		t.Errorf("usage = %v, want %v", got["usage"], want)
	}
}

func TestSetUsageRefusesACountTheClientRejects(t *testing.T) {
	const maxSafe = 1<<53 - 1
	for _, tc := range []struct {
		name  string
		usage aguix.TokenUsage
		ok    bool
	}{
		{"the largest safe count", aguix.TokenUsage{InputTokens: maxSafe}, true},
		{"one past it", aguix.TokenUsage{InputTokens: maxSafe + 1}, false},
		{"a negative part", aguix.TokenUsage{InputTokens: 1, CacheWriteInputTokens: -1}, false},
		{"past it on an entry that would be dropped", aguix.TokenUsage{TotalTokens: maxSafe + 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream, err := aguix.NewStream(httptest.NewRecorder())
			if err != nil {
				t.Fatal(err)
			}
			if err := stream.SetUsage(tc.usage); (err == nil) != tc.ok {
				t.Errorf("SetUsage = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

// waitThenFail emits opening frames, then waits on its context as an agent
// mid-call does, and returns what the context says.
func waitThenFail(opening ...aguix.Event) aguix.Agent {
	return aguix.AgentFunc(func(ctx context.Context, _ aguix.RunInput, out *aguix.Stream) error {
		for _, event := range opening {
			if err := out.Emit(event); err != nil {
				return err
			}
		}
		if err := out.SetUsage(aguix.TokenUsage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7}); err != nil {
			return err
		}
		<-ctx.Done()
		return fmt.Errorf("model call: %w", ctx.Err())
	})
}

// A run the handler's deadline stops is a cancel, with every span the agent
// left open closed first; the client's own verifier reads each transcript.
func TestARunPastItsTimeoutIsCancelled(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opening []aguix.Event
	}{
		{"timeout-mid-message", []aguix.Event{
			aguix.TextMessageStart("m1"), aguix.TextMessageContent("m1", "Checking the"),
		}},
		{"timeout-mid-reasoning", []aguix.Event{
			aguix.ReasoningStart("r1"), aguix.ReasoningMessageStart("r1"),
			aguix.ReasoningMessageContent("r1", "Stock first"),
		}},
		{"timeout-mid-tool-calls", []aguix.Event{
			aguix.TextMessageStart("m1"), aguix.TextMessageEnd("m1"),
			aguix.ToolCallStart("c1", "borrow_book", "m1"), aguix.ToolCallArgs("c1", []byte(`{"book_id":4}`)),
			aguix.ToolCallStart("c2", "borrow_book", "m1"),
			aguix.ToolCallEnd("c1"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reported error
			rec := run(t, waitThenFail(tc.opening...), declared,
				aguix.WithRunTimeout(10*time.Millisecond),
				aguix.WithOnError(func(_ *http.Request, err error) { reported = err }))

			golden(t, tc.name, rec.Body.String())
			got := lastFrame(t, rec)
			if want := outboundFixture(t, "run-finished-cancelled-usage"); !reflect.DeepEqual(got, want) {
				t.Errorf("last frame = %v, want %v", got, want)
			}
			if !errors.Is(reported, context.DeadlineExceeded) {
				t.Errorf("reported = %v, want the agent's error", reported)
			}
		})
	}
}

func TestATimeoutClosesOnlyWhatIsOpen(t *testing.T) {
	rec := run(t, waitThenFail(
		aguix.ToolCallStart("c1", "a", ""), aguix.ToolCallStart("c2", "b", ""),
		aguix.ToolCallEnd("c1"), aguix.ToolCallEnd("c9"),
	), declared, aguix.WithRunTimeout(10*time.Millisecond))

	got := types(frames(t, rec.Body.String()))
	want := []string{
		"RUN_STARTED", "TOOL_CALL_START", "TOOL_CALL_START", "TOOL_CALL_END", "TOOL_CALL_END",
		"TOOL_CALL_END", "RUN_FINISHED",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("frames = %v, want %v", got, want)
	}
	if closing := frames(t, rec.Body.String())[5]; closing["toolCallId"] != "c2" {
		t.Errorf("the handler closed %v, want c2", closing["toolCallId"])
	}
}

// A client that declares no protocol version has a schema that rejects the
// cancelled outcome and drops the stream, so it gets a bare finish.
func TestATimeoutForAnUndeclaredClientFinishesBare(t *testing.T) {
	rec := run(t, waitThenFail(aguix.TextMessageStart("m1")), oneTurn,
		aguix.WithRunTimeout(10*time.Millisecond))
	got := lastFrame(t, rec)
	want := map[string]any{
		"type": "RUN_FINISHED", "threadId": "t1", "runId": "r1",
		"usage": []any{map[string]any{
			"inputTokens": float64(5), "outputTokens": float64(2), "totalTokens": float64(7),
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("last frame = %v, want %v", got, want)
	}
}

func TestAnAgentsOwnCancelIsGatedTheSameWay(t *testing.T) {
	agent := aguix.AgentFunc(func(_ context.Context, _ aguix.RunInput, out *aguix.Stream) error {
		return out.Finish(aguix.Cancelled())
	})
	if got := lastFrame(t, run(t, agent, declared)); !reflect.DeepEqual(got, outboundFixture(t, "run-finished-cancelled")) {
		t.Errorf("declared: last frame = %v", got)
	}
	if got := lastFrame(t, run(t, agent, oneTurn)); got["outcome"] != nil {
		t.Errorf("undeclared: last frame = %v, want no outcome", got)
	}
}

func TestARunInsideItsTimeoutFinishesAsTheAgentSaid(t *testing.T) {
	agent := aguix.AgentFunc(func(_ context.Context, _ aguix.RunInput, out *aguix.Stream) error {
		return out.Finish(aguix.Success("c1"))
	})
	got := lastFrame(t, run(t, agent, declared, aguix.WithRunTimeout(time.Hour)))
	if outcome, _ := got["outcome"].(map[string]any); outcome["type"] != "success" {
		t.Errorf("last frame = %v, want the agent's success", got)
	}
}

func TestAZeroTimeoutIsNoBound(t *testing.T) {
	agent := aguix.AgentFunc(func(ctx context.Context, _ aguix.RunInput, _ *aguix.Stream) error {
		if _, ok := ctx.Deadline(); ok {
			return errors.New("the run has a deadline")
		}
		return nil
	})
	if got := lastFrame(t, run(t, agent, oneTurn, aguix.WithRunTimeout(0))); got["type"] != "RUN_FINISHED" {
		t.Errorf("last frame = %v", got)
	}
}

// A deadline the handler did not set is the agent's failure, even when the
// handler has a timeout of its own that has not passed.
func TestADeadlineFromOutsideIsATimedOutError(t *testing.T) {
	h, err := aguix.Handler(waitThenFail(), aguix.WithRunTimeout(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/agent", strings.NewReader(declared)))

	got := lastFrame(t, rec)
	if got["type"] != "RUN_ERROR" || got["code"] != aguix.CodeTimedOut {
		t.Errorf("last frame = %v, want RUN_ERROR timed_out", got)
	}
}

// A client that hung up is told nothing, since nobody is reading; the operator
// still hears of the run.
func TestAClientThatHungUpGetsNoTerminalFrame(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var reported error
	agent := aguix.AgentFunc(func(ctx context.Context, _ aguix.RunInput, out *aguix.Stream) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	})
	h, err := aguix.Handler(agent, aguix.WithRunTimeout(time.Hour),
		aguix.WithOnError(func(_ *http.Request, err error) { reported = err }))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodPost, "/agent", strings.NewReader(declared)))

	if got := types(frames(t, rec.Body.String())); !reflect.DeepEqual(got, []string{"RUN_STARTED"}) {
		t.Errorf("frames = %v, want only RUN_STARTED", got)
	}
	if !errors.Is(reported, context.Canceled) {
		t.Errorf("reported = %v, want the cancellation", reported)
	}
}

// failsOnce fails the write after the first n and no other, so a handler that
// kept writing past the failure would be seen doing it.
type failsOnce struct {
	*httptest.ResponseRecorder
	n int
}

func (w *failsOnce) Write(p []byte) (int, error) {
	w.n--
	if w.n == -1 {
		return 0, errors.New("broken pipe")
	}
	return w.ResponseRecorder.Write(p)
}

// A connection that breaks while the handler closes the open spans gets no
// RUN_FINISHED over a half-closed run.
func TestATimeoutStopsWhenClosingFails(t *testing.T) {
	h, err := aguix.Handler(waitThenFail(aguix.ToolCallStart("c1", "a", ""), aguix.ToolCallStart("c2", "b", "")),
		aguix.WithRunTimeout(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	w := &failsOnce{ResponseRecorder: httptest.NewRecorder(), n: 4}
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(declared)))

	got := types(frames(t, w.Body.String()))
	if want := []string{"RUN_STARTED", "TOOL_CALL_START", "TOOL_CALL_START", "TOOL_CALL_END"}; !reflect.DeepEqual(got, want) {
		t.Errorf("frames = %v, want %v", got, want)
	}
}

func TestEmitIsSafeFromManyGoroutines(t *testing.T) {
	rec := httptest.NewRecorder()
	stream, err := aguix.NewStream(rec)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("c%d", i)
			_ = stream.Emit(aguix.ToolCallStart(id, "a", ""))
			_ = stream.Emit(aguix.ToolCallEnd(id))
		}()
	}
	wg.Wait()
	if got := len(frames(t, rec.Body.String())); got != 40 {
		t.Errorf("%d frames, want 40", got)
	}
}
