package aguiadk

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/agenttool"
	"google.golang.org/genai"
)

func fixedIDs(t *testing.T) {
	t.Helper()
	n := 0
	was := newActivityID
	newActivityID = func() string {
		n++
		return strings.Repeat(string(rune('0'+n)), 8)
	}
	t.Cleanup(func() { newActivityID = was })
}

func encoded(t *testing.T, e aguix.Event) string {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPublishedActivitiesAreSnapshotsTheComponentDraws(t *testing.T) {
	ctx, queued := withSink(context.Background())
	if ok, err := PublishSuggestions(ctx, "", "  Update the address too ", "Show my loans"); !ok || err != nil {
		t.Fatalf("suggestions: %v %v", ok, err)
	}
	spec := ChartSpec{
		Labels: []string{"Mon", "Tue"}, Series: []ChartSeries{{Label: "Loans", Points: []float64{3, 4.5}}},
		Metadata: map[string]any{"title": "dropped", "unit": "books"},
	}
	if ok, err := PublishChart(ctx, "chart-loans", spec); !ok || err != nil {
		t.Fatalf("chart: %v %v", ok, err)
	}
	spec.Kind, spec.Title = "line", "Loans"
	if ok, err := PublishChart(ctx, "chart-loans", spec); !ok || err != nil {
		t.Fatalf("titled chart: %v %v", ok, err)
	}
	if !PublishCompaction(ctx, 12, 5) || PublishCompaction(ctx, 5, 5) {
		t.Error("compaction reported the wrong listener")
	}
	got := joined(queued.drain(nil))
	if len(got) != 4 {
		t.Fatalf("queued %d frames", len(got))
	}
	uuid := regexp.MustCompile(`^suggestions-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !uuid.MatchString(got[0].MessageID) {
		t.Errorf("suggestions id %q", got[0].MessageID)
	}
	got[0].MessageID, got[3].MessageID = "s", "c"
	for i, want := range []string{
		`{"type":"ACTIVITY_SNAPSHOT","messageId":"s","activityType":"suggestions","replace":true,` +
			`"content":{"prompts":["Update the address too","Show my loans"]}}`,
		`{"type":"ACTIVITY_SNAPSHOT","messageId":"chart-loans","activityType":"chart","replace":true,` +
			`"content":{"kind":"bar","labels":["Mon","Tue"],"series":[{"label":"Loans","points":[3,4.5]}],"unit":"books"}}`,
		`{"type":"ACTIVITY_SNAPSHOT","messageId":"chart-loans","activityType":"chart","replace":true,` +
			`"content":{"kind":"line","labels":["Mon","Tue"],"series":[{"label":"Loans","points":[3,4.5]}],` +
			`"title":"Loans","unit":"books"}}`,
		`{"type":"ACTIVITY_SNAPSHOT","messageId":"c","activityType":"compaction","replace":true,` +
			`"content":{"after":5,"before":12,"removed":7}}`,
	} {
		if e := encoded(t, got[i]); e != want {
			t.Errorf("frame %d:\n got %s\nwant %s", i, e, want)
		}
	}
	if spec.Metadata["title"] != "dropped" {
		t.Error("the chart edited the caller's metadata")
	}
}

func TestActivitiesOutsideARunQueueNothing(t *testing.T) {
	ctx := context.Background()
	if ok, err := PublishSuggestions(ctx, "", "Go on"); ok || err != nil {
		t.Errorf("suggestions: %v %v", ok, err)
	}
	if ok, err := PublishChart(ctx, "", ChartSpec{Labels: []string{"a"}, Series: []ChartSeries{{Points: []float64{1}}}}); ok || err != nil {
		t.Errorf("chart: %v %v", ok, err)
	}
	if PublishCompaction(ctx, 3, 1) {
		t.Error("compaction reported a listener")
	}
}

func TestActivitiesTheComponentWouldDropAreRefused(t *testing.T) {
	ctx, queued := withSink(context.Background())
	for name, prompts := range map[string][]string{
		"none":  nil,
		"five":  {"a", "b", "c", "d", "e"},
		"blank": {"fine", "  "},
		"long":  {strings.Repeat("é", MaxSuggestionChars+1)},
	} {
		if _, err := PublishSuggestions(ctx, "", prompts...); err == nil {
			t.Errorf("%s suggestions were accepted", name)
		}
	}
	if _, err := PublishSuggestions(ctx, "", strings.Repeat("é", MaxSuggestionChars)); err != nil {
		t.Errorf("a prompt at the limit was refused: %v", err)
	}
	one := []ChartSeries{{Label: "s", Points: []float64{1}}}
	many := make([]string, MaxChartLabels+1)
	wide := make([]ChartSeries, MaxChartPoints/MaxChartLabels+1)
	for i := range wide {
		wide[i] = ChartSeries{Points: make([]float64, MaxChartLabels)}
	}
	for name, spec := range map[string]ChartSpec{
		"no labels":  {Series: one},
		"no series":  {Labels: []string{"a"}},
		"labels":     {Labels: many, Series: one},
		"points":     {Labels: make([]string, MaxChartLabels), Series: wide},
		"misaligned": {Labels: []string{"a", "b"}, Series: one},
		"nan":        {Labels: []string{"a"}, Series: []ChartSeries{{Points: []float64{math.NaN()}}}},
		"inf":        {Labels: []string{"a"}, Series: []ChartSeries{{Points: []float64{math.Inf(-1)}}}},
		"huge":       {Labels: []string{"a"}, Series: []ChartSeries{{Points: []float64{-2e15}}}},
	} {
		if _, err := PublishChart(ctx, "", spec); err == nil {
			t.Errorf("a chart with %s was accepted", name)
		}
	}
	if _, err := PublishChart(ctx, "", ChartSpec{Labels: []string{"a"}, Series: []ChartSeries{{Points: []float64{1e15}}}}); err != nil {
		t.Errorf("a point at the limit was refused: %v", err)
	}
	if got := joined(queued.drain(nil)); len(got) != 2 {
		t.Errorf("queued %d frames, want the two at the limits", len(got))
	}
}

// A tool's activities follow its result; a model callback's precede the model's
// answer.
func TestActivitiesStreamWhereTheyWerePublished(t *testing.T) {
	fixedIDs(t)
	reg := services.New[struct{}](nil)
	services.MustRegister(reg, services.Spec[struct{}, borrowIn, borrowOut]{
		Name: "chart_loans", Description: "Chart the loans.", Kind: services.Query,
		Run: func(c services.Ctx[struct{}], in borrowIn) (borrowOut, error) {
			if _, err := PublishChart(c.Context, "", ChartSpec{
				Labels: []string{"Mon"}, Series: []ChartSeries{{Label: "Loans", Points: []float64{float64(in.BookID)}}},
			}); err != nil {
				return borrowOut{}, err
			}
			_, err := PublishSuggestions(c.Context, "", "Chart next week too")
			return borrowOut{Loan: in.BookID}, err
		},
	})
	ts, err := adkx.Toolset(reg, adkx.Anonymous)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	a, err := llmagent.New(llmagent.Config{
		Name: "librarian", Toolsets: []tool.Toolset{ts},
		Model: &scriptLLM{steps: []step{
			{calls: []*genai.FunctionCall{{ID: "c1", Name: "chart_loans", Args: map[string]any{"book_id": 3}}}},
			{text: "Charted."},
		}},
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{
			func(ctx agent.Context, _ *model.LLMRequest) (*model.LLMResponse, error) {
				if calls++; calls == 2 {
					PublishCompaction(ctx, 9, 4)
				}
				return nil, nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := serve(t, mustNew(t, a), input("run-act", text("user", "Chart my loans.")))
	wantTypes(t, s, "RUN_STARTED", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END",
		"TOOL_CALL_RESULT", "ACTIVITY_SNAPSHOT", "ACTIVITY_SNAPSHOT", "ACTIVITY_SNAPSHOT",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END", "RUN_FINISHED")
	for i, kind := range []string{"chart", "suggestions", "compaction"} {
		if got := s.frames[5+i]["activityType"]; got != kind {
			t.Errorf("activity %d is %v, want %s", i, got, kind)
		}
	}
	golden(t, "activity", s)
}

// A sub-agent agenttool runs reports each call it makes on the delegating
// call's card while that call runs, and its writes wait for that call's result.
func TestADelegatedSubAgentsCallsStreamAsProgress(t *testing.T) {
	sh := &shelf{}
	researcher, err := llmagent.New(llmagent.Config{
		Name: "researcher", Description: "Shelves books.", Toolsets: []tool.Toolset{sh.toolset(t)},
		Model: &scriptLLM{steps: []step{
			{calls: []*genai.FunctionCall{shelveBook("r1", 7)}},
			{calls: []*genai.FunctionCall{{ID: "r2", Name: "missing_tool"}}},
			{text: "Shelved it."},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := llmagent.New(llmagent.Config{
		Name: "librarian", Tools: []tool.Tool{agenttool.New(researcher, nil)},
		Model: &scriptLLM{steps: []step{
			{calls: []*genai.FunctionCall{{ID: "d1", Name: "researcher", Args: map[string]any{"request": "Shelve 7."}}}},
			{text: "Done."},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := serve(t, mustNew(t, root), input("run-sub", text("user", "Have the researcher shelve 7.")))
	wantTypes(t, s, "RUN_STARTED", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END",
		"CUSTOM", "CUSTOM", "CUSTOM", "CUSTOM", "TOOL_CALL_RESULT", "CUSTOM",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END", "RUN_FINISHED")
	got := customs(s)
	at := func(fragment string) int {
		for i, c := range got {
			if strings.Contains(c, fragment) {
				return i
			}
		}
		t.Errorf("no frame carries %s in\n%s", fragment, strings.Join(got, "\n"))
		return -1
	}
	for _, tc := range []struct{ call, result string }{
		{
			`"phase":"tool_call","status":"researcher: calling shelve_book","tool":{"name":"shelve_book","ok":null,"toolCallId":"r1"}`,
			`"phase":"tool_result","status":"researcher: shelve_book returned","tool":{"name":"shelve_book","ok":true,"toolCallId":"r1"}`,
		},
		{
			`"phase":"tool_call","status":"researcher: calling missing_tool","tool":{"name":"missing_tool","ok":null,"toolCallId":"r2"}`,
			`"phase":"tool_result","status":"researcher: missing_tool failed","tool":{"name":"missing_tool","ok":false,"toolCallId":"r2"}`,
		},
	} {
		if call, result := at(tc.call), at(tc.result); call > result {
			t.Errorf("a result preceded its call:\n%s", strings.Join(got, "\n"))
		}
	}
	for _, c := range got[:4] {
		if !strings.Contains(c, `{"name":"ag_ui.subagent","type":"CUSTOM","value":{"agent":"researcher","delegationId":"d1"`) {
			t.Errorf("progress frame %s", c)
		}
	}
	if !strings.Contains(got[4], `"name":"ag_ui.invalidate"`) {
		t.Errorf("the sub-agent's write was not sent after the delegation's result: %s", got[4])
	}
	golden(t, "subagent", s)
}
