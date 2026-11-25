package aguiadk

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagents/sequentialagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/agenttool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

func helper(t *testing.T, name string, llm model.LLM, tools ...tool.Tool) tool.Tool {
	t.Helper()
	a, err := llmagent.New(llmagent.Config{Name: name, Description: "Helps.", Model: llm, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	return agenttool.New(a, nil)
}

func boss(t *testing.T, llm model.LLM, tools ...tool.Tool) aguix.Agent {
	t.Helper()
	root, err := llmagent.New(llmagent.Config{Name: "librarian", Model: llm, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	return mustNew(t, root)
}

func ask(id, agentName string) *genai.FunctionCall {
	return &genai.FunctionCall{ID: id, Name: agentName, Args: map[string]any{"request": "Go."}}
}

// lifecycle is the run's SUBAGENT_* frames, each as its JSON with sorted keys.
func lifecycle(t *testing.T, s served) []string {
	t.Helper()
	var out []string
	for _, f := range s.frames {
		if !strings.HasPrefix(f["type"].(string), "SUBAGENT_") {
			continue
		}
		raw, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(raw))
	}
	return out
}

func wantLifecycle(t *testing.T, s served, want ...string) {
	t.Helper()
	if got := lifecycle(t, s); !slices.Equal(got, want) {
		t.Errorf("lifecycle:\n got %s\nwant %s\nbody:\n%s",
			strings.Join(got, "\n     "), strings.Join(want, "\n     "), s.body)
	}
}

// A sub-agent whose model fails ends in SUBAGENT_ERROR, never the provider's
// words, and the run goes on.
func TestAFailedDelegationEndsInSubagentError(t *testing.T) {
	helps := helper(t, "researcher", &scriptLLM{steps: []step{{code: "500"}}})
	a := boss(t, &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{ask("d1", "researcher")}}, {text: "Sorry."}}},
		helps)
	s := serve(t, a, input("run-subfail", text("user", "Delegate.")))
	wantTypes(t, s, "RUN_STARTED", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END",
		"SUBAGENT_STARTED", "SUBAGENT_ERROR", "TOOL_CALL_RESULT",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END", "RUN_FINISHED")
	wantLifecycle(t, s,
		`{"name":"researcher","parentToolCallId":"d1","subagentRunId":"subagent-d1","type":"SUBAGENT_STARTED"}`,
		`{"message":"researcher failed","subagentRunId":"subagent-d1","type":"SUBAGENT_ERROR"}`)
	golden(t, "subagent-failed", s)
}

// A sub-agent that delegates in turn nests the inner invocation in its own.
func TestANestedDelegationNamesItsParentSubagent(t *testing.T) {
	scribe := helper(t, "scribe", &scriptLLM{steps: []step{{text: "Noted."}}})
	researcher := helper(t, "researcher",
		&scriptLLM{steps: []step{{calls: []*genai.FunctionCall{ask("r1", "scribe")}}, {text: "Found."}}}, scribe)
	a := boss(t, &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{ask("d1", "researcher")}}, {text: "Done."}}},
		researcher)
	s := serve(t, a, input("run-subnest", text("user", "Delegate.")))
	wantTypes(t, s, "RUN_STARTED", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END",
		"SUBAGENT_STARTED", "CUSTOM", "SUBAGENT_STARTED", "SUBAGENT_FINISHED", "CUSTOM",
		"SUBAGENT_FINISHED", "TOOL_CALL_RESULT",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END", "RUN_FINISHED")
	wantLifecycle(t, s,
		`{"name":"researcher","parentToolCallId":"d1","subagentRunId":"subagent-d1","type":"SUBAGENT_STARTED"}`,
		`{"name":"scribe","parentSubagentRunId":"subagent-d1","parentToolCallId":"r1",`+
			`"subagentRunId":"subagent-r1","type":"SUBAGENT_STARTED"}`,
		`{"subagentRunId":"subagent-r1","type":"SUBAGENT_FINISHED"}`,
		`{"subagentRunId":"subagent-d1","type":"SUBAGENT_FINISHED"}`)
	golden(t, "subagent-nested", s)
}

// A model that reuses a call id still gets one id per invocation, since the
// client refuses a run that starts the same sub-agent id twice.
func TestARepeatedCallIDGetsItsOwnSubagentRun(t *testing.T) {
	helps := helper(t, "researcher", &scriptLLM{})
	a := boss(t, &scriptLLM{steps: []step{
		{calls: []*genai.FunctionCall{ask("call_0", "researcher")}},
		{calls: []*genai.FunctionCall{ask("call_0", "researcher")}},
		{text: "Done."},
	}}, helps)
	s := serve(t, a, input("run-subagain", text("user", "Delegate twice.")))
	wantLifecycle(t, s,
		`{"name":"researcher","parentToolCallId":"call_0","subagentRunId":"subagent-call_0","type":"SUBAGENT_STARTED"}`,
		`{"subagentRunId":"subagent-call_0","type":"SUBAGENT_FINISHED"}`,
		`{"name":"researcher","parentToolCallId":"call_0","subagentRunId":"subagent-call_0-2","type":"SUBAGENT_STARTED"}`,
		`{"subagentRunId":"subagent-call_0-2","type":"SUBAGENT_FINISHED"}`)
}

// A delegation the run's timeout cuts off says it was cancelled, once.
func TestADelegationCutOffByTheRunTimeoutIsCancelled(t *testing.T) {
	helps := helper(t, "researcher", &scriptLLM{steps: []step{{block: true}}})
	a := boss(t, &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{ask("d1", "researcher")}}}}, helps)
	s := serveWith(t, a, input("run-subcut", text("user", "Delegate.")),
		[]aguix.Option{aguix.WithRunTimeout(200 * time.Millisecond)})
	wantLifecycle(t, s,
		`{"name":"researcher","parentToolCallId":"d1","subagentRunId":"subagent-d1","type":"SUBAGENT_STARTED"}`,
		`{"code":"cancelled","message":"researcher was cancelled","subagentRunId":"subagent-d1","type":"SUBAGENT_ERROR"}`)
	golden(t, "subagent-cancelled", s)
}

// A delegated agent that runs further agents is still one invocation, under
// its own name.
func TestADelegatedWorkflowAgentIsOneInvocation(t *testing.T) {
	var steps []agent.Agent
	for _, name := range []string{"drafter", "checker"} {
		a, err := llmagent.New(llmagent.Config{Name: name, Model: &scriptLLM{}})
		if err != nil {
			t.Fatal(err)
		}
		steps = append(steps, a)
	}
	pipeline, err := sequentialagent.New(sequentialagent.Config{
		AgentConfig: agent.Config{Name: "pipeline", Description: "Drafts, then checks.", SubAgents: steps},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := boss(t, &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{ask("d1", "pipeline")}}, {text: "Done."}}},
		agenttool.New(pipeline, nil))
	wantLifecycle(t, serve(t, a, input("run-subflow", text("user", "Draft."))),
		`{"name":"pipeline","parentToolCallId":"d1","subagentRunId":"subagent-d1","type":"SUBAGENT_STARTED"}`,
		`{"subagentRunId":"subagent-d1","type":"SUBAGENT_FINISHED"}`)
}

// A tool that is not an agent opens no sub-agent.
func TestAnOrdinaryCallStartsNoSubagent(t *testing.T) {
	type in struct{}
	look, err := functiontool.New(functiontool.Config{Name: "look", Description: "Looks."},
		func(agent.Context, in) (map[string]any, error) { return map[string]any{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	a := boss(t, &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{{ID: "c1", Name: "look"}}}, {text: "Done."}}},
		look)
	wantLifecycle(t, serve(t, a, input("run-nosub", text("user", "Look."))))
}
