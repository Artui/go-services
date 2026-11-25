package aguix

import (
	"context"
	"fmt"
	"net/http"
)

// WithObserver hands fn every frame the client received, in wire order, from
// RUN_STARTED to RUN_FINISHED (with its usage) or RUN_ERROR.
//
// fn runs after the frame is written and flushed, while the frame still holds
// the stream, so frames from concurrent emitters reach it in the order they
// went out and a slow fn slows the run by its own time and no more. It cannot
// fail a run: it returns nothing, and a panic is recovered, reported once to
// WithOnError, and fn is not called again for that run. Its context is the
// request's without its cancellation, so a store can write after a hang-up.
// fn must not call the run's Stream: it runs holding the stream's lock, and
// Emit, SetUsage and Finish would wait on it forever.
func WithObserver(fn func(ctx context.Context, in RunInput, ev Event)) Option {
	return func(h *handler) { h.observer = fn }
}

// WithOnFinish hands fn the messages the run streamed, rebuilt from its frames,
// once the run is over: after RUN_FINISHED or RUN_ERROR, which it gets as end,
// or after the client hung up or the agent panicked, when end is the zero
// Event. end.FinishOutcome reads how a finished run ended. A run refused before
// its stream opened has no finish.
//
// The messages are this run's alone, assistant text and tool calls and tool
// results without reasoning, and nil when the run streamed none; the
// conversation before it is in.Messages, so a store saves both. The response
// stays open until fn returns, so the client's run ends after it: hand slow
// work to a goroutine. The context and the panic policy are WithObserver's,
// except that a finish hook is only ever called once.
func WithOnFinish(fn func(ctx context.Context, in RunInput, end Event, messages []Message)) Option {
	return func(h *handler) { h.onFinish = fn }
}

// watcher is one run's view of its own stream, for the two options above.
type watcher struct {
	h          *handler
	stream     *Stream
	r          *http.Request
	in         RunInput
	ctx        context.Context
	observer   func(context.Context, RunInput, Event)
	transcript *transcript
	end        Event
}

// watch attaches a watcher to the stream, or nothing when neither option is
// set, which leaves Emit exactly as it was.
func (h *handler) watch(r *http.Request, in RunInput, stream *Stream) *watcher {
	if h.observer == nil && h.onFinish == nil {
		return nil
	}
	w := &watcher{h: h, stream: stream, r: r, in: in, ctx: context.WithoutCancel(r.Context()), observer: h.observer}
	if h.onFinish != nil {
		w.transcript = newTranscript()
	}
	stream.observe = w.see
	return w
}

func (w *watcher) see(ev Event) {
	if ev.Type == EventRunFinished || ev.Type == EventRunError {
		w.end = ev
	}
	if w.transcript != nil {
		w.transcript.add(ev)
	}
	if w.observer != nil {
		w.call(ev)
	}
}

func (w *watcher) call(ev Event) {
	defer w.guard("observer", func() { w.observer = nil })
	w.observer(w.ctx, w.in, ev)
}

func (w *watcher) finish() {
	if w == nil || w.h.onFinish == nil {
		return
	}
	w.stream.mu.Lock()
	end, messages := w.end, w.transcript.messages()
	w.stream.mu.Unlock()
	defer w.guard("finish hook", func() {})
	w.h.onFinish(w.ctx, w.in, end, messages)
}

func (w *watcher) guard(what string, then func()) {
	if value := recover(); value != nil {
		then()
		w.h.report(w.r, fmt.Errorf("aguix: the %s panicked: %v", what, value))
	}
}
