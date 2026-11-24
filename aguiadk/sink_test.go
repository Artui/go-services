package aguiadk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/platform"
)

func joined(before, after []aguix.Event) []aguix.Event { return append(before, after...) }

func names(events []aguix.Event) []string {
	var out []string
	for _, e := range events {
		raw, _ := json.Marshal(e)
		var frame map[string]any
		_ = json.Unmarshal(raw, &frame)
		v, _ := frame["value"].(map[string]any)
		keys, _ := v["keys"].([]any)
		out = append(out, keys[0].(string))
	}
	return out
}

// inCall returns ctx as ADK's fan-out hands it to a call named id, derived once
// more as a spec's transaction would.
func inCall(ctx context.Context, id string) context.Context {
	var got context.Context
	platform.RunTasks(ctx, []func(context.Context){func(c context.Context) { got = c }})
	s := slotOf(got)
	s.id, s.name = id, "tool"
	return context.WithValue(got, txKey{}, "tx")
}

func TestACallsFramesWaitForItsAnswerAndTheRestGoNext(t *testing.T) {
	ctx, queued := withSink(context.Background())
	PublishInvalidation(inCall(ctx, "c1"), "", "c1")
	PublishInvalidation(inCall(ctx, "c2"), "", "c2")
	PublishInvalidation(ctx, "", "loose")
	if before, after := queued.drain([]string{"c3"}); !slices.Equal(names(before), []string{"loose"}) || len(after) != 0 {
		t.Errorf("an event answering another call released %v, then %v", names(before), names(after))
	}
	if before, after := queued.drain([]string{"c2"}); len(before) != 0 || !slices.Equal(names(after), []string{"c2"}) {
		t.Errorf("c2's answer released %v, then %v", names(before), names(after))
	}
	PublishInvalidation(inCall(ctx, "c1"), "", "c1 again")
	PublishInvalidation(inCall(ctx, "c4"), "", "c4")
	if got := names(queued.drainAll()); !slices.Equal(got, []string{"c1", "c1 again", "c4"}) {
		t.Errorf("the run's end released %v", got)
	}
	if got := queued.drainAll(); len(got) != 0 {
		t.Errorf("a drained sink still held %v", got)
	}
}

// A call a delegated sub-agent makes is held for the delegating call, the one
// whose result the client sees; a call not yet named counts as its parent.
func TestADelegatedCallsFramesWaitForTheDelegatingCall(t *testing.T) {
	ctx, queued := withSink(context.Background())
	outer := inCall(ctx, "outer")
	PublishInvalidation(inCall(outer, "inner"), "", "inner")
	var unnamed context.Context
	platform.RunTasks(outer, []func(context.Context){func(c context.Context) { unnamed = c }})
	PublishInvalidation(unnamed, "", "unnamed")
	if got := joined(queued.drain([]string{"inner"})); len(got) != 0 {
		t.Errorf("the inner call's answer released %v", names(got))
	}
	if got := names(joined(queued.drain([]string{"outer"}))); !slices.Equal(got, []string{"inner", "unnamed"}) {
		t.Errorf("the delegating call's answer released %v", got)
	}
}

// A tool run outside the bridge's fan-out has no slot; the plugin leaves it be.
func TestThePluginIgnoresACallWithoutASlot(t *testing.T) {
	p := sinkPlugin()
	ctx := &agent.ContextMock{}
	if r, err := p.BeforeToolCallback()(ctx, nil, nil); r != nil || err != nil {
		t.Errorf("before: %v %v", r, err)
	}
	if r, err := p.OnToolErrorCallback()(ctx, nil, nil, errors.New("boom")); r != nil || err != nil {
		t.Errorf("on error: %v %v", r, err)
	}
	if r, err := p.AfterToolCallback()(ctx, nil, nil, nil, nil); r != nil || err != nil {
		t.Errorf("after: %v %v", r, err)
	}
}

// A frame sent live after the run's last frames is dropped, not written past
// RUN_FINISHED.
func TestAFrameSentLiveAfterTheRunEndsIsDropped(t *testing.T) {
	rec := httptest.NewRecorder()
	stream, err := aguix.NewStream(rec)
	if err != nil {
		t.Fatal(err)
	}
	w := &wire{out: stream}
	if err := w.end(nil); err != nil {
		t.Fatal(err)
	}
	before := rec.Body.Len()
	w.live(aguix.Custom(SubagentEventName, nil))
	if rec.Body.Len() != before {
		t.Errorf("wrote after the end: %q", rec.Body.String()[before:])
	}
}
