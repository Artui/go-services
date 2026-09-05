package aguix

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	services "github.com/Artui/go-services"
)

// RunErrorText is what a client is told when a run fails for a reason outside
// the kernel's taxonomy.
//
// An agent's unexpected error is written for whoever is on call: it names
// hosts, models, quotas and internal state. RUN_ERROR's message is rendered in
// the chat, so putting the error there shows it to the user and, on a page
// where the assistant's output is read back by anything, to that too.
//
// The family's Python transport learned this the harder way: it redacts the
// same event, and the half that makes redaction affordable is the observer.
// Redacting with nowhere to send it means nobody ever sees the failure.
const RunErrorText = "The assistant could not complete that. " +
	"The reason was recorded on the server."

// Option configures a handler.
type Option func(*handler)

// WithOnError hands every failed run to fn, with the error as it actually was.
//
// It exists for the redaction above all: without it the run reports a fixed
// sentence to the client and drops the real failure, which is worth knowing
// before the first incident rather than during it.
func WithOnError(fn func(r *http.Request, err error)) Option {
	return func(h *handler) { h.onError = fn }
}

// WithMaxBodyBytes overrides the request-body ceiling.
//
// The default is the kernel's, because the size at which a request is refused
// is one decision for the library rather than a knob each transport sets
// differently. A run input carrying a long conversation is the reason this is
// adjustable at all.
func WithMaxBodyBytes(limit int64) Option {
	return func(h *handler) { h.maxBody = limit }
}

// Handler serves one agent over AG-UI.
//
// The wire is a POST carrying RunAgentInput and a Server-Sent Events response.
// The handler owns the run's lifecycle -- RUN_STARTED before the agent, and
// RUN_FINISHED or RUN_ERROR after it -- so an agent that returns early cannot
// leave a run open, and a client always sees a terminal event.
func Handler(agent Agent, opts ...Option) (http.Handler, error) {
	if agent == nil {
		return nil, errors.New("aguix: Handler needs an agent")
	}
	h := &handler{agent: agent, maxBody: services.DefaultMaxBodyBytes}
	for _, opt := range opts {
		opt(h)
	}
	return h, nil
}

type handler struct {
	agent   Agent
	maxBody int64
	onError func(*http.Request, error)
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Before the stream opens, because a failure here has no run to report it
	// on: a client that has not been told the response is a stream is still
	// reading an ordinary response and can be given an ordinary status.
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "a run must be posted", http.StatusMethodNotAllowed)
		return
	}

	input, err := h.decode(w, r)
	if err != nil {
		h.observe(r, err)
		http.Error(w, "the run input could not be read", http.StatusBadRequest)
		return
	}

	stream, err := NewStream(w)
	if err != nil {
		h.observe(r, err)
		http.Error(w, "this server cannot stream", http.StatusInternalServerError)
		return
	}

	// From here the status line is committed, so every remaining outcome is an
	// event rather than a status. A write failure means the client hung up and
	// there is nobody left to tell.
	if err := stream.Emit(RunStarted(input.ThreadID, input.RunID)); err != nil {
		h.observe(r, err)
		return
	}

	if err := h.agent.Run(r.Context(), input, stream); err != nil {
		h.observe(r, err)
		_ = stream.Emit(RunError(h.explain(err)))
		return
	}
	_ = stream.Emit(RunFinished(input.ThreadID, input.RunID))
}

// explain decides what the client is told about a failed run.
//
// The kernel's three client-facing sentinels keep their own words: a service
// that declined chose them for whoever made the call, and since kernel v0.4.0
// they carry no package prefix. Everything else is an operator's sentence and
// is replaced.
func (h *handler) explain(err error) string {
	switch {
	case errors.Is(err, services.ErrPermission),
		errors.Is(err, services.ErrNotFound),
		errors.Is(err, services.ErrConflict):
		return err.Error()
	}
	return RunErrorText
}

func (h *handler) decode(w http.ResponseWriter, r *http.Request) (RunInput, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBody))
	if err != nil {
		return RunInput{}, fmt.Errorf("aguix: reading the run input: %w", err)
	}

	var input RunInput
	if err := json.Unmarshal(body, &input); err != nil {
		return RunInput{}, fmt.Errorf("aguix: the run input is not valid JSON: %w", err)
	}
	if input.ThreadID == "" || input.RunID == "" {
		// Both are required, and both are echoed back on every lifecycle event.
		// A run with neither cannot be correlated by anything downstream.
		return RunInput{}, errors.New("aguix: the run input names no thread or run")
	}
	return input, nil
}

func (h *handler) observe(r *http.Request, err error) {
	if h.onError != nil {
		h.onError(r, err)
	}
}
