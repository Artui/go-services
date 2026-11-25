package aguix_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Artui/go-services/aguix"
)

// recorder keeps what an observer was handed, guarded because the stream may
// call it from whichever goroutine emitted.
type recorder struct {
	mu     sync.Mutex
	events []aguix.Event
	inputs []aguix.RunInput
}

func (rec *recorder) observe(_ context.Context, in aguix.RunInput, ev aguix.Event) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.events = append(rec.events, ev)
	rec.inputs = append(rec.inputs, in)
}

// wire re-frames the observed events the way the stream frames them, so an
// observer that saw anything other than the client's bytes fails byte for byte.
func (rec *recorder) wire(t *testing.T) string {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out strings.Builder
	for _, ev := range rec.events {
		payload, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("observed event does not encode: %v", err)
		}
		fmt.Fprintf(&out, "data: %s\n\n", payload)
	}
	return out.String()
}

func fullRun() aguix.Agent {
	return aguix.AgentFunc(func(_ context.Context, _ aguix.RunInput, out *aguix.Stream) error {
		for _, ev := range []aguix.Event{
			aguix.TextMessageStart("m1"),
			aguix.TextMessageContent("m1", "Let me "),
			aguix.TextMessageContent("m1", "check."),
			aguix.TextMessageEnd("m1"),
			aguix.ToolCallStart("c1", "lookup", "m1"),
			aguix.ToolCallArgs("c1", []byte(`{"id":4}`)),
			aguix.ToolCallEnd("c1"),
			aguix.ToolCallResult("t1", "c1", "not found", aguix.OutcomeFailed),
			aguix.TextMessageStart("m2"),
			aguix.TextMessageContent("m2", "It is gone."),
			aguix.TextMessageEnd("m2"),
		} {
			if err := out.Emit(ev); err != nil {
				return err
			}
		}
		if err := out.SetUsage(aguix.TokenUsage{
			Model: "m", InputTokens: 11, OutputTokens: 3, TotalTokens: 14, CacheWriteInputTokens: 2,
		}); err != nil {
			return err
		}
		return out.Finish(aguix.Success())
	})
}

func TestAnObserverSeesExactlyWhatTheClientParsed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent aguix.Agent
		body  string
		opts  []aguix.Option
		last  aguix.EventType
	}{
		{"finished", fullRun(), declared, nil, aguix.EventRunFinished},
		{"errored", failing(errors.New("model down")), oneTurn, nil, aguix.EventRunError},
		{"timed-out", waitThenFail(aguix.TextMessageStart("m1"), aguix.TextMessageContent("m1", "Checking")),
			declared, []aguix.Option{aguix.WithRunTimeout(10 * time.Millisecond)}, aguix.EventRunFinished},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quiet := aguix.WithOnError(func(*http.Request, error) {})
			bare := run(t, tc.agent, tc.body, append([]aguix.Option{quiet}, tc.opts...)...)

			seen := &recorder{}
			opts := append([]aguix.Option{quiet, aguix.WithObserver(seen.observe)}, tc.opts...)
			watched := run(t, tc.agent, tc.body, opts...)

			if watched.Body.String() != bare.Body.String() {
				t.Errorf("the observer changed the wire:\n%s\nwant:\n%s", watched.Body, bare.Body)
			}
			if got := seen.wire(t); got != watched.Body.String() {
				t.Errorf("observed:\n%s\nclient parsed:\n%s", got, watched.Body)
			}
			last := seen.events[len(seen.events)-1]
			if last.Type != tc.last {
				t.Errorf("last observed = %s, want %s", last.Type, tc.last)
			}
			if tc.name != "errored" && len(last.Usage) == 0 {
				t.Errorf("RUN_FINISHED reached the observer without its usage")
			}
			for _, in := range seen.inputs {
				if in.ThreadID != "t1" || in.RunID != "r1" {
					t.Errorf("observer got input %+v, want the run's", in)
				}
			}
		})
	}
}

func TestAnObserverSeesOnlyWhatWasWritten(t *testing.T) {
	seen := &recorder{}
	h, err := aguix.Handler(fullRun(),
		aguix.WithOnError(func(*http.Request, error) {}), aguix.WithObserver(seen.observe))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(&hangUp{ResponseWriter: rec, after: 3},
		httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(oneTurn)))

	if got := seen.wire(t); got != rec.Body.String() {
		t.Errorf("observed:\n%s\nwritten:\n%s", got, rec.Body)
	}
	if len(seen.events) != 3 {
		t.Errorf("observed %d events, want the 3 written", len(seen.events))
	}
}

// Frames emitted from several goroutines reach the observer in wire order,
// because it is called while the frame still holds the stream.
func TestAnObserverSeesConcurrentFramesInWireOrder(t *testing.T) {
	agent := aguix.AgentFunc(func(_ context.Context, _ aguix.RunInput, out *aguix.Stream) error {
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range 20 {
					_ = out.Emit(aguix.Custom("tick", fmt.Sprintf("%d-%d", i, j)))
				}
			}()
		}
		wg.Wait()
		return nil
	})
	seen := &recorder{}
	rec := run(t, agent, oneTurn, aguix.WithObserver(seen.observe))
	if got := seen.wire(t); got != rec.Body.String() {
		t.Errorf("observer order differs from the wire")
	}
}

// The panic policy: a panicking observer is recovered and reported to
// WithOnError once, is not called again for that run, and the run is untouched.
func TestAPanickingObserverCannotFailTheRun(t *testing.T) {
	bare := run(t, fullRun(), declared)

	calls := 0
	var reported []error
	rec := run(t, fullRun(), declared,
		aguix.WithOnError(func(_ *http.Request, err error) { reported = append(reported, err) }),
		aguix.WithObserver(func(context.Context, aguix.RunInput, aguix.Event) {
			calls++
			panic("observer bug")
		}))

	if rec.Body.String() != bare.Body.String() {
		t.Errorf("a panicking observer changed the wire:\n%s", rec.Body)
	}
	if calls != 1 {
		t.Errorf("observer called %d times after panicking, want 1", calls)
	}
	if len(reported) != 1 || !strings.Contains(reported[0].Error(), "observer bug") {
		t.Errorf("reported = %v, want the one panic", reported)
	}
}
