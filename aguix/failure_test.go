package aguix_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/aguix"
)

// hangUp is a client that goes away mid-stream: the status line is already
// committed, so every write after the first fails and there is nobody left to
// tell. Nothing here may panic or block on that.
type hangUp struct {
	http.ResponseWriter
	after int
	wrote int
}

func (h *hangUp) Write(p []byte) (int, error) {
	h.wrote++
	if h.wrote > h.after {
		return 0, errors.New("connection reset by peer")
	}
	return h.ResponseWriter.Write(p)
}

func (h *hangUp) Flush() {
	if f, ok := h.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func serveTo(t *testing.T, w http.ResponseWriter, agent aguix.Agent) {
	t.Helper()
	h, err := aguix.Handler(agent, aguix.WithOnError(func(*http.Request, error) {}))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(oneTurn)))
}

// A client hanging up part-way through is ordinary traffic, not a fault. The
// run stops writing and returns; it does not panic, and it does not spend the
// rest of the stream writing into a closed socket.
func TestAClientHangingUpEndsTheRunQuietly(t *testing.T) {
	box := toolboxFor(t, signedIn)
	agent := aguix.Scripted(aguix.Rule{Steps: []aguix.Step{
		aguix.Say("one two three four"),
		aguix.CallTool(box, "borrow_book", json.RawMessage(`{"book_id":4}`)),
	}})

	// Every prefix of the stream, so each write site is the one that fails.
	for after := 0; after < 8; after++ {
		t.Run(fmt.Sprintf("after %d writes", after), func(t *testing.T) {
			serveTo(t, &hangUp{ResponseWriter: httptest.NewRecorder(), after: after}, agent)
		})
	}
}

// A service returning something no encoder can represent is this process's bug.
// It is the one tool failure worth ending the run over: a client handed a
// result it cannot parse has no way to tell that from an empty answer.
func TestAnUnencodableToolResultEndsTheRun(t *testing.T) {
	type stats struct {
		Mean float64 `json:"mean"`
	}
	reg := services.New(resolve)
	services.MustRegister(reg, services.Spec[deps, borrowIn, stats]{
		Name: "average", Kind: services.Query,
		Run: func(services.Ctx[deps], borrowIn) (stats, error) {
			return stats{Mean: math.NaN()}, nil
		},
	})
	box, err := aguix.NewToolbox(reg, signedIn)
	if err != nil {
		t.Fatalf("NewToolbox: %v", err)
	}

	var reported error
	agent := aguix.Scripted(aguix.Rule{Steps: []aguix.Step{
		aguix.CallTool(box, "average", json.RawMessage(`{"book_id":1}`)),
	}})
	h, err := aguix.Handler(agent, aguix.WithOnError(
		func(_ *http.Request, err error) { reported = err }))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(oneTurn)))

	events := frames(t, rec.Body.String())
	last := events[len(events)-1]
	if last["type"] != "RUN_ERROR" {
		t.Errorf("run ended with %v, want RUN_ERROR", last["type"])
	}
	// The client still got a result frame, saying the operation failed, before
	// the run ended -- so the transcript is not a call with no answer. This is
	// the one failure path the toolbox's own tables cannot reach, because it
	// needs a service returning a value no encoder can represent, and it is
	// marked like every other: failed, because the fault is this process's and
	// nobody refused anything.
	result := events[len(events)-2]
	if got := fmt.Sprint(result["content"]); got != aguix.ToolResultError {
		t.Errorf("result = %q, want the fixed sentence", got)
	}
	if got := result["outcome"]; got != string(aguix.OutcomeFailed) {
		t.Errorf("outcome = %v, want %q", got, aguix.OutcomeFailed)
	}
	if reported == nil {
		t.Error("the encoding failure was not reported")
	}
}

// A rule whose When does not match is skipped, and the next one answers.
func TestTheFirstMatchingRuleWins(t *testing.T) {
	agent := aguix.Scripted(
		aguix.Rule{When: aguix.WhenUserSays("goodbye"), Steps: []aguix.Step{aguix.Say("bye")}},
		aguix.Rule{When: aguix.WhenUserSays("hello"), Steps: []aguix.Step{aguix.Say("hi")}},
		aguix.Rule{Steps: []aguix.Step{aguix.Say("fallback")}},
	)
	body := run(t, agent, oneTurn).Body.String()

	if !strings.Contains(body, `"hi"`) {
		t.Errorf("body = %s, want the matching rule to answer", body)
	}
	if strings.Contains(body, "bye") || strings.Contains(body, "fallback") {
		t.Error("more than one rule answered")
	}
}

// A run that matches nothing says nothing and finishes cleanly. An empty turn
// is a legitimate answer; a failure the client cannot act on is not.
func TestARunThatMatchesNothingStillFinishes(t *testing.T) {
	agent := aguix.Scripted(
		aguix.Rule{When: aguix.WhenUserSays("goodbye"), Steps: []aguix.Step{aguix.Say("bye")}},
	)
	got := types(frames(t, run(t, agent, oneTurn).Body.String()))
	if fmt.Sprint(got) != fmt.Sprint([]string{"RUN_STARTED", "RUN_FINISHED"}) {
		t.Errorf("events = %v", got)
	}
}

// A matcher needs a user message to match against, and a run may have none --
// a client opening a thread to let the agent speak first.
func TestAMatcherWithNoUserMessageDoesNotMatch(t *testing.T) {
	agent := aguix.Scripted(
		aguix.Rule{When: aguix.WhenUserSays("hello"), Steps: []aguix.Step{aguix.Say("hi")}},
	)
	const noUser = `{"threadId":"t1","runId":"r1","messages":[` +
		`{"id":"a1","role":"assistant","content":"hello"}]}`
	body := run(t, agent, noUser).Body.String()
	if strings.Contains(body, `"hi"`) {
		t.Error("a rule matched an assistant message")
	}
}

// A step that fails ends the run, and the failure is redacted like any other.
func TestAFailingStepEndsTheRun(t *testing.T) {
	agent := aguix.Scripted(aguix.Rule{Steps: []aguix.Step{
		aguix.Say("starting"),
		func(context.Context, aguix.RunInput, *aguix.Stream) error {
			return errors.New("the model provider timed out")
		},
		aguix.Say("never reached"),
	}})
	body := run(t, agent, oneTurn,
		aguix.WithOnError(func(*http.Request, error) {})).Body.String()

	if strings.Contains(body, "never reached") {
		t.Error("a step after the failing one ran")
	}
	events := frames(t, body)
	if last := events[len(events)-1]; last["type"] != "RUN_ERROR" {
		t.Errorf("run ended with %v, want RUN_ERROR", last["type"])
	}
}

// CallToolFrom builds its arguments from the run, which is what lets a scripted
// agent answer what was typed rather than recite.
func TestCallToolFromReadsTheRun(t *testing.T) {
	box := toolboxFor(t, signedIn)
	agent := aguix.Scripted(aguix.Rule{Steps: []aguix.Step{
		aguix.CallToolFrom(box, "borrow_book", func(in aguix.RunInput) (json.RawMessage, error) {
			message, _ := in.LastUserMessage()
			if strings.Contains(message.Content, "hello") {
				return json.RawMessage(`{"book_id":4}`), nil
			}
			return nil, errors.New("nothing to borrow")
		}),
	}})

	events := frames(t, run(t, agent, oneTurn).Body.String())
	if got := fmt.Sprint(events[2]["delta"]); got != `{"book_id":4}` {
		t.Errorf("args = %q, want them built from the message", got)
	}
}

func TestCallToolFromFailingEndsTheRun(t *testing.T) {
	box := toolboxFor(t, signedIn)
	agent := aguix.Scripted(aguix.Rule{Steps: []aguix.Step{
		aguix.CallToolFrom(box, "borrow_book", func(aguix.RunInput) (json.RawMessage, error) {
			return nil, errors.New("no book named")
		}),
	}})
	events := frames(t, run(t, agent, oneTurn,
		aguix.WithOnError(func(*http.Request, error) {})).Body.String())

	if last := events[len(events)-1]; last["type"] != "RUN_ERROR" {
		t.Errorf("run ended with %v, want RUN_ERROR", last["type"])
	}
}

// The same hang-up, against a run that is only a tool call.
//
// The test above puts a Say in front, so by the time the call's own frames go
// out the client has already been dropped at an earlier write. These are the
// write sites inside Call itself: the arguments, the end of the argument
// stream, and the result.
func TestAClientHangingUpDuringAToolCall(t *testing.T) {
	box := toolboxFor(t, signedIn)
	agent := aguix.Scripted(aguix.Rule{Steps: []aguix.Step{
		aguix.CallTool(box, "borrow_book", json.RawMessage(`{"book_id":4}`)),
	}})

	for after := 0; after < 5; after++ {
		t.Run(fmt.Sprintf("after %d writes", after), func(t *testing.T) {
			serveTo(t, &hangUp{ResponseWriter: httptest.NewRecorder(), after: after}, agent)
		})
	}
}

// A principal that refuses is not the same as a resolver that refuses: it
// happens before the registry is reached at all, and is still an answer the
// conversation can continue from.
func TestAPrincipalThatRefusesIsAResult(t *testing.T) {
	refusing := func(context.Context) (any, error) {
		return nil, fmt.Errorf("%w: this session expired", services.ErrPermission)
	}
	events := runTool(t, toolboxFor(t, refusing), `{"book_id":4}`)

	if last := events[len(events)-1]; last["type"] != "RUN_FINISHED" {
		t.Errorf("run ended with %v, want RUN_FINISHED", last["type"])
	}
	result := fmt.Sprint(events[len(events)-2]["content"])
	if !strings.Contains(result, "this session expired") {
		t.Errorf("result = %q, want the principal's own words", result)
	}
}
