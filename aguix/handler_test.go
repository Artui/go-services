package aguix_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/aguix"
)

// frames reads an SSE body back into the events it carried.
//
// The parse is deliberately literal -- split on the blank line, strip "data: "
// -- rather than a library's. What is being tested is that this package writes
// frames a client can read, and a shared parser would let both sides agree on
// something a client would reject.
func frames(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		if block == "" {
			continue
		}
		if !strings.HasPrefix(block, "data: ") {
			t.Fatalf("frame is not an SSE data line: %q", block)
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(block, "data: ")), &event); err != nil {
			t.Fatalf("frame is not JSON: %q", block)
		}
		out = append(out, event)
	}
	return out
}

func types(events []map[string]any) []string {
	var out []string
	for _, event := range events {
		out = append(out, fmt.Sprint(event["type"]))
	}
	return out
}

func run(t *testing.T, agent aguix.Agent, body string, opts ...aguix.Option) *httptest.ResponseRecorder {
	t.Helper()
	h, err := aguix.Handler(agent, opts...)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(body)))
	return rec
}

const oneTurn = `{"threadId":"t1","runId":"r1","messages":[{"id":"u1","role":"user","content":"hello"}]}`

// The handler owns the lifecycle, so an agent that emits nothing still produces
// a run a client can complete.
func TestARunIsAlwaysOpenedAndClosed(t *testing.T) {
	rec := run(t, aguix.AgentFunc(
		func(context.Context, aguix.RunInput, *aguix.Stream) error { return nil }), oneTurn)

	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q", got)
	}
	got := types(frames(t, rec.Body.String()))
	want := []string{"RUN_STARTED", "RUN_FINISHED"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestAnAgentsEventsAreStreamedBetweenThem(t *testing.T) {
	rec := run(t, aguix.Scripted(aguix.Rule{Steps: []aguix.Step{aguix.Say("hi there")}}), oneTurn)

	got := types(frames(t, rec.Body.String()))
	want := []string{
		"RUN_STARTED", "TEXT_MESSAGE_START",
		"TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_CONTENT",
		"TEXT_MESSAGE_END", "RUN_FINISHED",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

// An agent's unexpected error is written for an operator. RUN_ERROR's message
// is rendered in the chat, so it is replaced -- and reported, because redacting
// with nowhere to send it means nobody sees the failure at all.
func TestAnUnexpectedFailureIsRedactedAndReported(t *testing.T) {
	const operatorText = "dial tcp 10.0.0.4:11434: connection refused"

	var reported error
	rec := run(t, aguix.AgentFunc(func(context.Context, aguix.RunInput, *aguix.Stream) error {
		return errors.New(operatorText)
	}), oneTurn, aguix.WithOnError(func(_ *http.Request, err error) { reported = err }))

	events := frames(t, rec.Body.String())
	last := events[len(events)-1]
	if last["type"] != "RUN_ERROR" {
		t.Fatalf("last event = %v, want RUN_ERROR", last["type"])
	}
	if last["message"] != aguix.RunErrorText {
		t.Errorf("message = %v, want the fixed sentence", last["message"])
	}
	if strings.Contains(rec.Body.String(), operatorText) {
		t.Error("the operator's words reached the client")
	}
	if reported == nil || !strings.Contains(reported.Error(), operatorText) {
		t.Errorf("reported = %v, want the real error", reported)
	}
}

// The taxonomy's client-facing members keep their own words, because a service
// that declined chose them for whoever made the call.
func TestARefusalKeepsItsWords(t *testing.T) {
	rec := run(t, aguix.AgentFunc(func(context.Context, aguix.RunInput, *aguix.Stream) error {
		return fmt.Errorf("%w: this thread is read only", services.ErrPermission)
	}), oneTurn)

	events := frames(t, rec.Body.String())
	last := events[len(events)-1]
	if last["message"] != "permission denied: this thread is read only" {
		t.Errorf("message = %v, want the service's own words", last["message"])
	}
}

func TestTheRunIsRefusedBeforeAnyStream(t *testing.T) {
	for _, tc := range []struct {
		name, method, body string
		want               int
	}{
		{"a GET", http.MethodGet, oneTurn, http.StatusMethodNotAllowed},
		{"not JSON", http.MethodPost, "{", http.StatusBadRequest},
		{"no thread or run", http.MethodPost, `{"messages":[]}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := aguix.Handler(
				aguix.Scripted(), aguix.WithOnError(func(*http.Request, error) {}))
			if err != nil {
				t.Fatalf("Handler: %v", err)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, "/agent", strings.NewReader(tc.body)))

			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
			// A refusal before the stream opens is an ordinary response, not a
			// run with zero events: a client given a 200 and an empty stream
			// has no way to tell that from an agent with nothing to say.
			if got := rec.Header().Get("Content-Type"); strings.Contains(got, "event-stream") {
				t.Errorf("Content-Type = %q, want an ordinary response", got)
			}
		})
	}
}

func TestHandlerRefusesANilAgent(t *testing.T) {
	if _, err := aguix.Handler(nil); err == nil {
		t.Error("Handler accepted a nil agent")
	}
}

func TestABodyOverTheCeilingIsRefused(t *testing.T) {
	h, err := aguix.Handler(aguix.Scripted(), aguix.WithMaxBodyBytes(16),
		aguix.WithOnError(func(*http.Request, error) {}))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(oneTurn)))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// A ResponseWriter that cannot flush cannot stream, and that is the server's
// problem rather than the client's.
type unflushable struct{ http.ResponseWriter }

func TestAWriterThatCannotFlushIsAServerError(t *testing.T) {
	h, err := aguix.Handler(aguix.Scripted(), aguix.WithOnError(func(*http.Request, error) {}))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(unflushable{rec}, httptest.NewRequest(
		http.MethodPost, "/agent", strings.NewReader(oneTurn)))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
