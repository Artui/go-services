package main

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// byAgent answers as the librarian or the catalogue, told apart by their
// instructions: each makes its calls on its first turn, then says "Done.".
type byAgent struct {
	librarian, catalogue []*genai.FunctionCall

	mu   sync.Mutex
	seen []int
}

func (*byAgent) Name() string { return "byAgent" }

func (m *byAgent) GenerateContent(
	_ context.Context, req *model.LLMRequest, _ bool,
) iter.Seq2[*model.LLMResponse, error] {
	m.mu.Lock()
	m.seen = append(m.seen, len(req.Contents))
	m.mu.Unlock()
	calls := m.librarian
	if req.Config != nil && req.Config.SystemInstruction != nil &&
		strings.Contains(fmt.Sprint(req.Config.SystemInstruction.Parts[0].Text), "for the librarian") {
		calls = m.catalogue
	}
	parts := []*genai.Part{genai.NewPartFromText("Done.")}
	if last := req.Contents[len(req.Contents)-1]; len(calls) > 0 && last.Parts[0].FunctionResponse == nil {
		parts = nil
		for _, c := range calls {
			parts = append(parts, &genai.Part{FunctionCall: c})
		}
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{
			Content: &genai.Content{Role: genai.RoleModel, Parts: parts}, TurnComplete: true,
		}, nil)
	}
}

func ofType(frames []map[string]any, typ string) (at []int) {
	for i, f := range frames {
		if f["type"] == typ {
			at = append(at, i)
		}
	}
	return at
}

func resultOf(t *testing.T, frames []map[string]any, id string) int {
	t.Helper()
	for i, f := range frames {
		if f["type"] == "TOOL_CALL_RESULT" && f["toolCallId"] == id {
			return i
		}
	}
	t.Fatalf("no result for %s", id)
	return -1
}

func encoded(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// The chart and the follow-ups each reach the page as an activity after their
// own call's result, and the chart's data stays out of what the model hears.
func TestTheChartAndTheFollowUpsReachThePage(t *testing.T) {
	frames := run(t, &byAgent{librarian: []*genai.FunctionCall{
		{ID: "c-chart", Name: "chart_loans", Args: map[string]any{}},
		{ID: "c-suggest", Name: "suggest_follow_ups", Args: map[string]any{
			"prompts": []any{"Borrow book 10", "What do I owe?"},
		}},
	}}, newDB(t))

	got := map[string]map[string]any{}
	for _, i := range ofType(frames, "ACTIVITY_SNAPSHOT") {
		got[fmt.Sprint(frames[i]["activityType"])] = frames[i]
		call := map[string]string{"chart": "c-chart", "suggestions": "c-suggest"}[fmt.Sprint(frames[i]["activityType"])]
		if r := resultOf(t, frames, call); i < r {
			t.Errorf("%v (%d) precedes its call's result (%d)", frames[i]["activityType"], i, r)
		}
	}
	chart := encoded(got["chart"]["content"])
	if want := `"labels":["The Mythical Man-Month","Structure and Interpretation"]`; !strings.Contains(chart, want) ||
		!strings.Contains(chart, `"points":[1,1]`) || !strings.Contains(chart, `"title":"Loans per book"`) {
		t.Errorf("chart = %s", chart)
	}
	if s := encoded(got["suggestions"]["content"]); s != `{"prompts":["Borrow book 10","What do I owe?"]}` {
		t.Errorf("suggestions = %s", s)
	}
	if r := encoded(frames[resultOf(t, frames, "c-chart")]["content"]); strings.Contains(r, "Mythical") {
		t.Errorf("the model heard the chart's data: %s", r)
	}
	if _, ok := got["compaction"]; ok {
		t.Error("a short history was compacted")
	}
}

// A question the librarian hands the catalogue shows the catalogue's own calls
// on the delegating call's card, while that call runs.
func TestACatalogueDelegationShowsItsProgress(t *testing.T) {
	frames := run(t, &byAgent{
		librarian: []*genai.FunctionCall{{ID: "d1", Name: "catalogue", Args: map[string]any{"request": "Which books are in?"}}},
		catalogue: []*genai.FunctionCall{{ID: "k1", Name: "list_books", Args: map[string]any{}}},
	}, newDB(t))

	result := resultOf(t, frames, "d1")
	var steps []string
	for _, i := range ofType(frames, "CUSTOM") {
		if frames[i]["name"] != "ag_ui.subagent" {
			continue
		}
		if i > result {
			t.Errorf("progress at %d after the delegating call's result at %d", i, result)
		}
		v, _ := frames[i]["value"].(map[string]any)
		if v["delegationId"] != "d1" || v["agent"] != "catalogue" {
			t.Errorf("progress = %v", v)
		}
		steps = append(steps, fmt.Sprint(v["status"]))
	}
	if want := "[catalogue: calling list_books catalogue: list_books returned]"; fmt.Sprint(steps) != want {
		t.Errorf("steps = %v, want %s", steps, want)
	}
}

// A history past keepContents is cut at a member's message before the model
// sees it, and the page is told how much went.
func TestALongHistoryIsCompacted(t *testing.T) {
	var msgs []string
	for i := range 15 {
		msgs = append(msgs,
			fmt.Sprintf(`{"id":"u%d","role":"user","content":"question %d"}`, i, i),
			fmt.Sprintf(`{"id":"a%d","role":"assistant","content":"answer %d"}`, i, i))
	}
	msgs = append(msgs, `{"id":"u-last","role":"user","content":"and now?"}`)
	m := &byAgent{}
	frames := runBody(t, m, newDB(t),
		`{"threadId":"t","runId":"r","messages":[`+strings.Join(msgs, ",")+`]}`)

	snaps := ofType(frames, "ACTIVITY_SNAPSHOT")
	if len(snaps) != 1 || frames[snaps[0]]["activityType"] != "compaction" {
		t.Fatalf("activities = %v", snaps)
	}
	if c := encoded(frames[snaps[0]]["content"]); c != `{"after":19,"before":31,"removed":12}` {
		t.Errorf("compaction = %s", c)
	}
	if fmt.Sprint(m.seen) != "[19]" {
		t.Errorf("the model saw %v contents", m.seen)
	}
	if text := ofType(frames, "TEXT_MESSAGE_START"); len(text) != 1 || text[0] < snaps[0] {
		t.Errorf("the notice (%d) does not lead the answer (%v)", snaps[0], text)
	}
}
