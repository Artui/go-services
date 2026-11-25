package aguix_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Artui/go-services/aguix"
)

// An agent that panics still gets its finish, with the zero end, and its panic
// still reaches net/http.
func TestTheFinishHookRunsWhenTheAgentPanics(t *testing.T) {
	var got finished
	h, err := aguix.Handler(aguix.AgentFunc(func(_ context.Context, _ aguix.RunInput, out *aguix.Stream) error {
		for _, ev := range []aguix.Event{
			aguix.TextMessageStart("m1"), aguix.TextMessageContent("m1", "Half"),
		} {
			if err := out.Emit(ev); err != nil {
				return err
			}
		}
		panic("agent bug")
	}), aguix.WithOnFinish(got.hook))
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recovered := recover(); recovered != "agent bug" {
				t.Errorf("recovered %v, want the agent's panic", recovered)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(),
			httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(oneTurn)))
	}()
	if got.calls != 1 || got.end.Type != "" {
		t.Errorf("hook called %d times with end %q, want once with the zero Event", got.calls, got.end.Type)
	}
	if len(got.messages) != 1 || got.messages[0].ID != "m1" {
		t.Errorf("messages = %s, want the half-streamed m1", messagesJSON(t, got.messages))
	}
}

// An agent goroutine that outlives Run emits after RUN_FINISHED, while the
// finish hook reads the transcript; under -race this fails unless the read
// holds the stream's lock (most runs: the detector sees the race, not every time).
func TestTheFinishHookReadsTheTranscriptUnderTheStreamsLock(t *testing.T) {
	ended, done := make(chan struct{}), make(chan struct{})
	agent := aguix.AgentFunc(func(_ context.Context, _ aguix.RunInput, out *aguix.Stream) error {
		if err := out.Emit(aguix.TextMessageContent("m1", "x")); err != nil {
			return err
		}
		go func() {
			defer close(done)
			<-ended
			for range 200 {
				_ = out.Emit(aguix.TextMessageContent("m1", "x"))
			}
		}()
		return nil
	})
	var calls int
	run(t, agent, oneTurn,
		aguix.WithObserver(func(_ context.Context, _ aguix.RunInput, ev aguix.Event) {
			if ev.Type == aguix.EventRunFinished {
				close(ended)
			}
		}),
		aguix.WithOnFinish(func(context.Context, aguix.RunInput, aguix.Event, []aguix.Message) { calls++ }))
	<-done
	if calls != 1 {
		t.Errorf("hook called %d times, want once", calls)
	}
}
