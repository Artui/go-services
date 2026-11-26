package aguiadk

import (
	"context"
	"errors"
	"iter"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagents/parallelagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

// redactHooked calls before ahead of each request its model is sent.
type redactHooked struct {
	model.LLM
	before func(context.Context)
}

func (h redactHooked) GenerateContent(
	ctx context.Context, req *model.LLMRequest, stream bool,
) iter.Seq2[*model.LLMResponse, error] {
	h.before(ctx)
	return h.LLM.GenerateContent(ctx, req, stream)
}

func redactResult(t *testing.T, s served, call string) (string, bool) {
	t.Helper()
	for _, f := range s.frames {
		if f["type"] == "TOOL_CALL_RESULT" && f["toolCallId"] == call {
			return f["content"].(string), true
		}
	}
	return "", false
}

func TestSubagentRedactAFailedDelegationsResultSaysWhatSubagentErrorSays(t *testing.T) {
	helps := helper(t, "researcher", &scriptLLM{steps: []step{{code: "500"}}})
	a := boss(t, &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{ask("d1", "researcher")}}, {text: "Sorry."}}},
		helps)
	s := serve(t, a, input("run-subfail", text("user", "Delegate.")))
	got, ok := redactResult(t, s, "d1")
	if want := `{"error":"researcher failed"}`; !ok || got != want {
		t.Errorf("result %q, want %q\nbody:\n%s", got, want, s.body)
	}
	if strings.Contains(s.body, "provider detail") || strings.Contains(s.body, "500") {
		t.Errorf("the provider's words reached the client:\n%s", s.body)
	}
}

func TestSubagentRedactACancelledBranchNeverQuotesItsSiblingsError(t *testing.T) {
	const runs = 25
	seen := 0
	for range runs {
		entered := make(chan struct{})
		researcher := helper(t, "researcher", redactHooked{&scriptLLM{}, func(ctx context.Context) {
			close(entered)
			<-ctx.Done()
		}})
		lender, err := llmagent.New(llmagent.Config{
			Name: "lender", Tools: []tool.Tool{researcher},
			Model: &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{ask("p1", "researcher")}}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		shelver, err := llmagent.New(llmagent.Config{
			Name: "shelver",
			Model: redactHooked{&scriptLLM{steps: []step{{err: errors.New("sibling secret")}}}, func(context.Context) {
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Error("the delegation never started")
				}
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		root, err := parallelagent.New(parallelagent.Config{
			AgentConfig: agent.Config{Name: "both", SubAgents: []agent.Agent{lender, shelver}},
		})
		if err != nil {
			t.Fatal(err)
		}
		s := serve(t, mustNew(t, root), input("run-subredact", text("user", "Both.")))
		if strings.Contains(s.body, "sibling secret") {
			t.Fatalf("the sibling's error reached the client:\n%s", s.body)
		}
		if got, ok := redactResult(t, s, "p1"); ok {
			seen++
			if want := `{"error":"researcher was cancelled"}`; got != want {
				t.Fatalf("cancelled result %q, want %q\nbody:\n%s", got, want, s.body)
			}
		}
	}
	if seen == 0 {
		t.Fatalf("no run of %d yielded the cancelled branch's result, so nothing was tested", runs)
	}
}

func TestSubagentRedactADelegationCancelledBeforeItsSubagentStartsNeverQuotesItsSibling(t *testing.T) {
	const runs = 20
	seen := 0
	for range runs {
		inside, enter := signal()
		researcher := helper(t, "researcher", &scriptLLM{steps: []step{{text: "Done."}}})
		lender, err := llmagent.New(llmagent.Config{
			Name: "lender", Tools: []tool.Tool{researcher},
			Model: &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{ask("p1", "researcher")}}}},
			BeforeToolCallbacks: []llmagent.BeforeToolCallback{
				func(ctx agent.Context, _ tool.Tool, _ map[string]any) (map[string]any, error) {
					enter()
					<-ctx.Done()
					return nil, nil
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		shelver, err := llmagent.New(llmagent.Config{
			Name: "shelver",
			Model: hooked{&scriptLLM{steps: []step{{err: errors.New("sibling secret")}}},
				func(context.Context) { await(t, inside) }},
		})
		if err != nil {
			t.Fatal(err)
		}
		s := serve(t, mustNew(t, both(t, lender, shelver)), input("run-subredact", text("user", "Both.")))
		if strings.Contains(s.body, "sibling secret") {
			t.Fatalf("the sibling's error reached the client:\n%s", s.body)
		}
		if got, ok := redactResult(t, s, "p1"); ok {
			seen++
			if want := `{"error":"researcher was cancelled"}`; got != want {
				t.Fatalf("cancelled result %q, want %q\nbody:\n%s", got, want, s.body)
			}
		}
	}
	if seen == 0 {
		t.Fatalf("no run of %d yielded the cancelled call's result, so nothing was tested", runs)
	}
}

func TestSubagentRedactTwoFailedDelegationsSharingACallIDAreBothRedacted(t *testing.T) {
	helps := helper(t, "researcher", &scriptLLM{steps: []step{{code: "500"}, {code: "500"}}})
	a := boss(t, &scriptLLM{steps: []step{
		{calls: []*genai.FunctionCall{ask("d1", "researcher"), ask("d1", "researcher")}}, {text: "Sorry."},
	}}, helps)
	s := serve(t, a, input("run-subtwice", text("user", "Delegate twice.")))
	var got []string
	for _, f := range s.frames {
		if f["type"] == "TOOL_CALL_RESULT" {
			got = append(got, f["content"].(string))
		}
	}
	want := `{"error":"researcher failed"}`
	if len(got) != 2 || got[0] != want || got[1] != want {
		t.Errorf("results %q, want two of %q\nbody:\n%s", got, want, s.body)
	}
	if strings.Contains(s.body, "provider detail") {
		t.Errorf("the provider's words reached the client:\n%s", s.body)
	}
}

func redactResults(s served) []string {
	var out []string
	for _, f := range s.frames {
		if f["type"] == "TOOL_CALL_RESULT" {
			out = append(out, f["content"].(string))
		}
	}
	return out
}

func TestSubagentRedactASucceededDelegationLeavesNoFailureForItsCallID(t *testing.T) {
	researcher := helper(t, "researcher", &scriptLLM{steps: []step{{text: "Found."}}})
	scribe := helper(t, "scribe", &scriptLLM{steps: []step{{code: "500"}}})
	a := boss(t, &scriptLLM{steps: []step{
		{calls: []*genai.FunctionCall{ask("call_0", "researcher")}},
		{calls: []*genai.FunctionCall{ask("call_0", "scribe")}},
		{text: "Sorry."},
	}}, researcher, scribe)
	s := serve(t, a, input("run-subreuse", text("user", "Delegate twice.")))
	got := redactResults(s)
	if want := []string{`{"result":"Found."}`, `{"error":"scribe failed"}`}; !slices.Equal(got, want) {
		t.Errorf("results %q, want %q\nbody:\n%s", got, want, s.body)
	}
}

func TestSubagentRedactAFailureInsideADelegationLeavesNoFailureForAnOuterCall(t *testing.T) {
	type in struct{}
	look, err := functiontool.New(functiontool.Config{Name: "look", Description: "Looks."},
		func(agent.Context, in) (map[string]any, error) { return nil, errors.New("looked badly") })
	if err != nil {
		t.Fatal(err)
	}
	scribe := helper(t, "scribe", &scriptLLM{steps: []step{{code: "500"}}})
	researcher := helper(t, "researcher",
		&scriptLLM{steps: []step{{calls: []*genai.FunctionCall{ask("r1", "scribe")}}, {text: "Found."}}}, scribe)
	a := boss(t, &scriptLLM{steps: []step{
		{calls: []*genai.FunctionCall{ask("d1", "researcher")}},
		{calls: []*genai.FunctionCall{{ID: "r1", Name: "look"}}},
		{text: "Done."},
	}}, researcher, look)
	s := serve(t, a, input("run-subinner", text("user", "Delegate, then look.")))
	got := redactResults(s)
	if len(got) != 2 || strings.Contains(got[1], "scribe") {
		t.Errorf("results %q: the outer call took the inner failure\nbody:\n%s", got, s.body)
	}
}
