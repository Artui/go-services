package aguiadk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagents/parallelagent"
	"google.golang.org/adk/v2/agent/workflowagents/sequentialagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/platform"
	"google.golang.org/adk/v2/session"
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

// A call id shaped like a numbered one is still not issued twice.
func TestANumberedSubagentRunSkipsAnIDAlreadyIssued(t *testing.T) {
	helps := helper(t, "researcher", &scriptLLM{})
	a := boss(t, &scriptLLM{steps: []step{
		{calls: []*genai.FunctionCall{ask("x-2", "researcher")}},
		{calls: []*genai.FunctionCall{ask("x", "researcher")}},
		{calls: []*genai.FunctionCall{ask("x", "researcher")}},
		{text: "Done."},
	}}, helps)
	var got []string
	for _, f := range serve(t, a, input("run-subclash", text("user", "Delegate."))).frames {
		if f["type"] == "SUBAGENT_STARTED" {
			got = append(got, f["subagentRunId"].(string))
		}
	}
	if want := []string{"subagent-x-2", "subagent-x", "subagent-x-3"}; !slices.Equal(got, want) {
		t.Errorf("sub-agent run ids %v, want %v", got, want)
	}
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

// hooked is a model that runs enter ahead of each answer, which is how a test
// puts the frames of concurrent calls into one order.
type hooked struct {
	model.LLM
	enter func(context.Context)
}

func (m hooked) GenerateContent(
	ctx context.Context, req *model.LLMRequest, stream bool,
) iter.Seq2[*model.LLMResponse, error] {
	m.enter(ctx)
	return m.LLM.GenerateContent(ctx, req, stream)
}

// signal returns a channel and the function that closes it, safe to call twice.
func signal() (<-chan struct{}, func()) {
	ch := make(chan struct{})
	return ch, sync.OnceFunc(func() { close(ch) })
}

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Error("a gate never opened")
	}
}

// inTurn is a root that makes calls d1 and d2 in one step: d2 starts once
// first is open, and done is called when d1 has returned.
func inTurn(t *testing.T, first <-chan struct{}, done func(), tools ...tool.Tool) aguix.Agent {
	t.Helper()
	root, err := llmagent.New(llmagent.Config{
		Name: "librarian", Tools: tools,
		Model: &scriptLLM{steps: []step{
			{calls: []*genai.FunctionCall{ask("d1", "researcher"), ask("d2", "scribe")}}, {text: "Done."},
		}},
		BeforeToolCallbacks: []llmagent.BeforeToolCallback{
			func(ctx agent.Context, _ tool.Tool, _ map[string]any) (map[string]any, error) {
				if ctx.FunctionCallID() == "d2" {
					await(t, first)
				}
				return nil, nil
			},
		},
		AfterToolCallbacks: []llmagent.AfterToolCallback{
			func(ctx agent.Context, _ tool.Tool, _, _ map[string]any, _ error) (map[string]any, error) {
				if ctx.FunctionCallID() == "d1" {
					done()
				}
				return nil, nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return mustNew(t, root)
}

// A transfer hands the run to another agent, which is not a delegation.
func TestATransferStartsNoSubagent(t *testing.T) {
	other, err := llmagent.New(llmagent.Config{Name: "researcher", Description: "Helps.", Model: &scriptLLM{}})
	if err != nil {
		t.Fatal(err)
	}
	llm := &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{
		{ID: "x1", Name: "transfer_to_agent", Args: map[string]any{"agent_name": "researcher"}},
	}}}}
	root, err := llmagent.New(llmagent.Config{Name: "librarian", Model: llm, SubAgents: []agent.Agent{other}})
	if err != nil {
		t.Fatal(err)
	}
	s := serve(t, mustNew(t, root), input("run-subtransfer", text("user", "Hand over.")))
	wantTypes(t, s, "RUN_STARTED", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END", "RUN_FINISHED")
	wantLifecycle(t, s)
	golden(t, "subagent-transfer", s)
}

// fixed is a toolset of the tools it holds, so that Gate can wrap an agenttool.
type fixed []tool.Tool

func (fixed) Name() string { return "fixed" }

func (f fixed) Tools(agent.ReadonlyContext) ([]tool.Tool, error) { return f, nil }

// A gated delegation opens nothing while it waits for the person, and opens and
// closes on the run that carries the approval.
func TestAGatedDelegationOpensOnTheResumeRun(t *testing.T) {
	sub := &scriptLLM{}
	approval := NewApproval(nil, Require("researcher"))
	build := func(llm *scriptLLM) aguix.Agent {
		root, err := llmagent.New(llmagent.Config{
			Name: "librarian", Model: llm,
			Toolsets: []tool.Toolset{approval.Gate(fixed{helper(t, "researcher", sub)})},
		})
		if err != nil {
			t.Fatal(err)
		}
		return mustNew(t, root, WithApproval(approval))
	}

	paused := serve(t, build(&scriptLLM{steps: []step{{calls: []*genai.FunctionCall{ask("d1", "researcher")}}}}),
		input("run-subgate", text("user", "Delegate.")))
	wantTypes(t, paused, "RUN_STARTED", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "RUN_FINISHED")
	wantLifecycle(t, paused)
	if sub.calls() != 0 {
		t.Errorf("the sub-agent ran before approval")
	}
	golden(t, "subagent-gated-pause", paused)

	in := inbound(t, "approve")
	in.RunID = "run-subgated"
	var call []aguix.Message
	if err := json.Unmarshal([]byte(`[{"id":"u1","role":"user","content":"Delegate."},`+
		`{"id":"d1","role":"assistant","toolCalls":[{"id":"d1","type":"function",`+
		`"function":{"name":"researcher","arguments":"{\"request\":\"Go.\"}"}}]}]`), &call); err != nil {
		t.Fatal(err)
	}
	in.Messages = call
	in.Resume[0].InterruptID = "int-d1"
	resumedRun := serve(t, build(&scriptLLM{}), in)
	wantLifecycle(t, resumedRun,
		`{"name":"researcher","parentToolCallId":"d1","subagentRunId":"subagent-d1","type":"SUBAGENT_STARTED"}`,
		`{"subagentRunId":"subagent-d1","type":"SUBAGENT_FINISHED"}`)
	if sub.calls() != 1 {
		t.Errorf("the sub-agent's model was called %d times, want 1", sub.calls())
	}
	golden(t, "subagent-gated-resume", resumedRun)
}

// Each branch of a ParallelAgent delegates under its own id, while the other's
// sub-agent is still open.
func TestEachParallelBranchClosesItsDelegation(t *testing.T) {
	firstIn, enterFirst := signal()
	secondIn, enterSecond := signal()
	firstDone, finishFirst := signal()

	researcher := helper(t, "researcher", hooked{&scriptLLM{}, func(context.Context) {
		enterFirst()
		await(t, secondIn)
	}})
	scribe := helper(t, "scribe", hooked{&scriptLLM{}, func(context.Context) {
		enterSecond()
		await(t, firstDone)
	}})
	lender, err := llmagent.New(llmagent.Config{
		Name: "lender", Tools: []tool.Tool{researcher},
		Model: &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{ask("p1", "researcher")}}}},
		AfterAgentCallbacks: []agent.AfterAgentCallback{func(agent.Context) (*genai.Content, error) {
			finishFirst()
			return nil, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	shelver, err := llmagent.New(llmagent.Config{
		Name: "shelver", Tools: []tool.Tool{scribe},
		Model: hooked{
			&scriptLLM{steps: []step{{calls: []*genai.FunctionCall{ask("p2", "scribe")}}}},
			func(context.Context) { await(t, firstIn) },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := serve(t, mustNew(t, both(t, lender, shelver)), input("run-subpar", text("user", "Both.")))
	wantLifecycle(t, s,
		`{"name":"researcher","parentToolCallId":"p1","subagentRunId":"subagent-p1","type":"SUBAGENT_STARTED"}`,
		`{"name":"scribe","parentToolCallId":"p2","subagentRunId":"subagent-p2","type":"SUBAGENT_STARTED"}`,
		`{"subagentRunId":"subagent-p1","type":"SUBAGENT_FINISHED"}`,
		`{"subagentRunId":"subagent-p2","type":"SUBAGENT_FINISHED"}`)
	golden(t, "subagent-parallel", s)
}

func both(t *testing.T, agents ...agent.Agent) agent.Agent {
	t.Helper()
	a, err := parallelagent.New(parallelagent.Config{AgentConfig: agent.Config{Name: "both", SubAgents: agents}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Two delegations of one step are open together, and each closes.
func TestTwoDelegationsInOneStepBothClose(t *testing.T) {
	firstIn, enterFirst := signal()
	secondIn, enterSecond := signal()
	firstDone, finishFirst := signal()
	researcher := helper(t, "researcher", hooked{&scriptLLM{}, func(context.Context) {
		enterFirst()
		await(t, secondIn)
	}})
	scribe := helper(t, "scribe", hooked{&scriptLLM{}, func(context.Context) {
		enterSecond()
		await(t, firstDone)
	}})
	s := serve(t, inTurn(t, firstIn, finishFirst, researcher, scribe),
		input("run-subpair", text("user", "Delegate both.")))
	wantLifecycle(t, s,
		`{"name":"researcher","parentToolCallId":"d1","subagentRunId":"subagent-d1","type":"SUBAGENT_STARTED"}`,
		`{"name":"scribe","parentToolCallId":"d2","subagentRunId":"subagent-d2","type":"SUBAGENT_STARTED"}`,
		`{"subagentRunId":"subagent-d1","type":"SUBAGENT_FINISHED"}`,
		`{"subagentRunId":"subagent-d2","type":"SUBAGENT_FINISHED"}`)
	golden(t, "subagent-pair", s)
}

// The run's timeout ends every open delegation as cancelled.
func TestTwoDelegationsCutOffByTheRunTimeoutAreBothCancelled(t *testing.T) {
	firstIn, enterFirst := signal()
	firstDone, finishFirst := signal()
	researcher := helper(t, "researcher", hooked{&scriptLLM{}, func(ctx context.Context) {
		enterFirst()
		<-ctx.Done()
	}})
	scribe := helper(t, "scribe", hooked{&scriptLLM{}, func(ctx context.Context) {
		<-ctx.Done()
		await(t, firstDone)
	}})
	s := serveWith(t, inTurn(t, firstIn, finishFirst, researcher, scribe),
		input("run-subpaircut", text("user", "Delegate both.")),
		[]aguix.Option{aguix.WithRunTimeout(500 * time.Millisecond)})
	wantLifecycle(t, s,
		`{"name":"researcher","parentToolCallId":"d1","subagentRunId":"subagent-d1","type":"SUBAGENT_STARTED"}`,
		`{"name":"scribe","parentToolCallId":"d2","subagentRunId":"subagent-d2","type":"SUBAGENT_STARTED"}`,
		`{"code":"cancelled","message":"researcher was cancelled","subagentRunId":"subagent-d1","type":"SUBAGENT_ERROR"}`,
		`{"code":"cancelled","message":"scribe was cancelled","subagentRunId":"subagent-d2","type":"SUBAGENT_ERROR"}`)
	golden(t, "subagent-pair-cancelled", s)
}

// quietWhenCancelled runs a, dropping what it yields once cancelled: ADK picks at
// random whether a cancelled branch's last event still reaches the run.
func quietWhenCancelled(t *testing.T, a agent.Agent) agent.Agent {
	t.Helper()
	quiet, err := agent.New(agent.Config{
		Name: "quiet", SubAgents: []agent.Agent{a},
		Run: func(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				for ev, err := range a.Run(ctx) {
					if ctx.Err() != nil || !yield(ev, err) {
						return
					}
				}
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return quiet
}

// A ParallelAgent branch that fails cancels its sibling, whose delegation says
// so before the run does.
func TestASiblingFailingMidDelegationClosesItBeforeRunError(t *testing.T) {
	inside, enter := signal()
	researcher := helper(t, "researcher", hooked{&scriptLLM{}, func(ctx context.Context) {
		enter()
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
		Model: hooked{
			&scriptLLM{steps: []step{{err: errors.New("provider detail")}}},
			func(context.Context) { await(t, inside) },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := serve(t, mustNew(t, both(t, quietWhenCancelled(t, lender), shelver)),
		input("run-subsibling", text("user", "Both.")))
	wantTypes(t, s, "RUN_STARTED", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END",
		"SUBAGENT_STARTED", "SUBAGENT_ERROR", "RUN_ERROR")
	wantLifecycle(t, s,
		`{"name":"researcher","parentToolCallId":"p1","subagentRunId":"subagent-p1","type":"SUBAGENT_STARTED"}`,
		`{"code":"cancelled","message":"researcher was cancelled","subagentRunId":"subagent-p1","type":"SUBAGENT_ERROR"}`)
	golden(t, "subagent-sibling-failed", s)
}

// countedIDs numbers ADK's ids, one of which the failed delegation's result
// quotes, so that its transcript is the same on every run.
func countedIDs(next http.Handler) http.Handler {
	var n atomic.Int64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := platform.WithUUIDProvider(r.Context(), func() string { return fmt.Sprint(n.Add(1)) })
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// A sub-agent cannot wait for a person, so a gated tool inside it fails the
// delegation without running.
func TestAGatedToolInsideASubagentEndsInSubagentError(t *testing.T) {
	a := newArchive(t)
	approval := NewApproval(a.entries)
	inner, err := llmagent.New(llmagent.Config{
		Name: "researcher", Description: "Helps.",
		Model:    &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{discardCall("c1", 7)}}}},
		Toolsets: []tool.Toolset{approval.Gate(a.ts)},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := boss(t, &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{ask("d1", "researcher")}}}},
		agenttool.New(inner, nil))
	s := serve(t, root, input("run-subinner", text("user", "Delegate.")), countedIDs)
	wantLifecycle(t, s,
		`{"name":"researcher","parentToolCallId":"d1","subagentRunId":"subagent-d1","type":"SUBAGENT_STARTED"}`,
		`{"message":"researcher failed","subagentRunId":"subagent-d1","type":"SUBAGENT_ERROR"}`)
	if ran := a.ran(); len(ran) != 0 {
		t.Errorf("the gated spec ran: %v", ran)
	}
	golden(t, "subagent-gated-inside", s)
}

// A sub-agent its own callback skips was still delegated to.
func TestASkippedSubagentStillOpensAndCloses(t *testing.T) {
	sub := &scriptLLM{}
	inner, err := llmagent.New(llmagent.Config{
		Name: "researcher", Description: "Helps.", Model: sub,
		BeforeAgentCallbacks: []agent.BeforeAgentCallback{func(agent.Context) (*genai.Content, error) {
			return genai.NewContentFromText("Skipped.", genai.RoleModel), nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := boss(t, &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{ask("d1", "researcher")}}}},
		agenttool.New(inner, nil))
	s := serve(t, a, input("run-subskip", text("user", "Delegate.")))
	wantLifecycle(t, s,
		`{"name":"researcher","parentToolCallId":"d1","subagentRunId":"subagent-d1","type":"SUBAGENT_STARTED"}`,
		`{"subagentRunId":"subagent-d1","type":"SUBAGENT_FINISHED"}`)
	if sub.calls() != 0 {
		t.Errorf("the skipped sub-agent's model was called")
	}
	golden(t, "subagent-skipped", s)
}

// A browser call in the same step leaves the run pending without holding the
// delegation open.
func TestABrowserCallBesideADelegationStillClosesIt(t *testing.T) {
	llm := &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{ask("d1", "researcher"), fillCall("f1")}}}}
	root, err := llmagent.New(llmagent.Config{
		Name: "librarian", Model: llm,
		Tools:    []tool.Tool{helper(t, "researcher", &scriptLLM{})},
		Toolsets: []tool.Toolset{ClientToolset()},
	})
	if err != nil {
		t.Fatal(err)
	}
	in := input("run-subbrowser", text("user", "Delegate and fill."))
	in.Tools = []aguix.ToolDefinition{fillField}
	s := serve(t, mustNew(t, root), in)
	wantLifecycle(t, s,
		`{"name":"researcher","parentToolCallId":"d1","subagentRunId":"subagent-d1","type":"SUBAGENT_STARTED"}`,
		`{"subagentRunId":"subagent-d1","type":"SUBAGENT_FINISHED"}`)
	if llm.calls() != 1 {
		t.Errorf("the model was called %d times, want 1 with the browser call pending", llm.calls())
	}
	golden(t, "subagent-browser-tool", s)
}
