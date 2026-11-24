package aguiadk

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

type noArgs struct{}

// countTurn reads the seeded "turns", which a client's JSON leaves a json.Number.
func countTurn(t *testing.T) tool.Tool {
	t.Helper()
	tl, err := functiontool.New(functiontool.Config{Name: "count_turn", Description: "Count a turn."},
		func(ctx agent.Context, _ noArgs) (map[string]any, error) {
			n := int64(0)
			if v, err := ctx.State().Get("turns"); err == nil {
				n, _ = v.(json.Number).Int64()
			}
			if err := ctx.State().Set("turns", n+1); err != nil {
				return nil, err
			}
			if err := ctx.State().Set("temp:scratch", "invocation only"); err != nil {
				return nil, err
			}
			return map[string]any{"turns": n + 1}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

func stateAgent(t *testing.T, llm model.LLM) agent.Agent {
	t.Helper()
	a, err := llmagent.New(llmagent.Config{
		Name: "librarian", Model: llm, Instruction: "Draft: {draft?}. Turns: {turns?}.",
		OutputKey: "summary", Tools: []tool.Tool{countTurn(t)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func countCall(id string) *genai.FunctionCall {
	return &genai.FunctionCall{ID: id, Name: "count_turn", Args: map[string]any{}}
}

// producerInput reads a run body @ag-ui/client wrote for aguix (scripts/agui-wire).
func producerInput(t *testing.T, name string) aguix.RunInput {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "aguix", "testdata", "agui", "inbound", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var in aguix.RunInput
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	return in
}

func instruction(req *model.LLMRequest) string {
	if req.Config == nil || req.Config.SystemInstruction == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range req.Config.SystemInstruction.Parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// applyState is the client's side: a snapshot replaces the state, and each
// "add" of a STATE_DELTA sets one top-level member, the only shape this bridge sends.
func applyState(t *testing.T, state map[string]any, s served) map[string]any {
	t.Helper()
	for _, f := range s.frames {
		switch f["type"] {
		case "STATE_SNAPSHOT":
			state = map[string]any{}
			for k, v := range f["snapshot"].(map[string]any) {
				state[k] = v
			}
		case "STATE_DELTA":
			for _, raw := range f["delta"].([]any) {
				op := raw.(map[string]any)
				path := op["path"].(string)
				if op["op"] != "add" || strings.Count(path, "/") != 1 {
					t.Fatalf("unexpected operation %v", op)
				}
				key := strings.NewReplacer("~1", "/", "~0", "~").Replace(path[1:])
				state[key] = op["value"]
			}
		}
	}
	return state
}

func TestStateRoundTripsThroughARun(t *testing.T) {
	llm := &scriptLLM{steps: []step{
		{calls: []*genai.FunctionCall{countCall("c1")}},
		{text: "Noted."},
		{text: "Still sunset."},
	}}
	b := mustNew(t, stateAgent(t, llm))

	first := producerInput(t, "reasoning")
	s := serve(t, b, first)
	wantTypes(t, s, "RUN_STARTED", "STATE_SNAPSHOT",
		"TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT", "STATE_DELTA",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END", "STATE_DELTA",
		"RUN_FINISHED")
	if got := instruction(llm.request(0)); !strings.HasPrefix(got, "Draft: sunset. Turns: 2.\n") {
		t.Errorf("the model saw %q", got)
	}
	if strings.Contains(s.body, "temp:") {
		t.Errorf("an invocation-only key reached the client:\n%s", s.body)
	}
	golden(t, "state-seeded", s)

	state := applyState(t, nil, s)
	want := map[string]any{"draft": "sunset", "turns": float64(3), "summary": "Noted."}
	if got, _ := json.Marshal(state); string(got) != mustJSON(t, want) {
		t.Errorf("state after the run = %s, want %s", got, mustJSON(t, want))
	}

	second := input("run-2", text("user", "And now?"))
	second.State = json.RawMessage(mustJSON(t, state))
	s = serve(t, b, second)
	if got := instruction(llm.request(llm.calls() - 1)); !strings.HasPrefix(got, "Draft: sunset. Turns: 3.\n") {
		t.Errorf("the next run's model saw %q", got)
	}
	golden(t, "state-next", s)
	if got := applyState(t, nil, s)["summary"]; got != "Still sunset." {
		t.Errorf("summary after the next run = %v", got)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestEmptyStatePatchesWithoutASnapshot(t *testing.T) {
	llm := &scriptLLM{steps: []step{{text: "Hello."}}}
	in := input("run-1", text("user", "Hi."))
	in.State = json.RawMessage(`{}`)
	s := serve(t, mustNew(t, stateAgent(t, llm)), in)
	wantTypes(t, s, "RUN_STARTED",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END", "STATE_DELTA", "RUN_FINISHED")
	if got := mustJSON(t, s.first("STATE_DELTA")["delta"]); got != `[{"op":"add","path":"/summary","value":"Hello."}]` {
		t.Errorf("delta = %s", got)
	}
}

func TestNoStateSendsTheFirstWriteAsASnapshot(t *testing.T) {
	llm := &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{countCall("c1")}}, {text: "Counted."}}}
	s := serve(t, mustNew(t, stateAgent(t, llm)), input("run-1", text("user", "Count.")))
	wantTypes(t, s, "RUN_STARTED",
		"TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT", "STATE_SNAPSHOT",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END", "STATE_DELTA", "RUN_FINISHED")
	if got := mustJSON(t, s.first("STATE_SNAPSHOT")["snapshot"]); got != `{"turns":1}` {
		t.Errorf("snapshot = %s", got)
	}
	if got := applyState(t, nil, s); mustJSON(t, got) != `{"summary":"Counted.","turns":1}` {
		t.Errorf("state = %v", got)
	}
}

func TestStateThatIsNotAnObjectIsARunError(t *testing.T) {
	for _, state := range []string{`[1]`, `"draft"`, `3`} {
		t.Run(state, func(t *testing.T) {
			llm := &scriptLLM{}
			in := input("run-1", text("user", "Hi."))
			in.State = json.RawMessage(state)
			s := serve(t, mustNew(t, stateAgent(t, llm)), in)
			wantTypes(t, s, "RUN_STARTED", "RUN_ERROR")
			wantRunError(t, s, "state is not a JSON object")
			if llm.calls() != 0 {
				t.Errorf("the model was called")
			}
		})
	}
}

func TestStateOnAConversationEndingOnTheAssistantSendsNothing(t *testing.T) {
	llm := &scriptLLM{}
	in := input("run-1", text("user", "Hi."), text("assistant", "Hello."))
	in.State = json.RawMessage(`{"draft":"sunset"}`)
	s := serve(t, mustNew(t, stateAgent(t, llm)), in)
	wantTypes(t, s, "RUN_STARTED", "RUN_FINISHED")
}

func TestAClientThatHangsUpBeforeTheSnapshotEndsTheRun(t *testing.T) {
	llm := &scriptLLM{steps: []step{{text: "Unused."}}}
	in := input("run-1", text("user", "Hi."))
	in.State = json.RawMessage(`{"draft":"sunset"}`)
	if err := runDirect(t, mustNew(t, stateAgent(t, llm)), 0, in); !errors.Is(err, errHungUp) {
		t.Errorf("err = %v", err)
	}
	if llm.calls() != 0 {
		t.Errorf("the model was called")
	}
}

func trState(partial bool, delta map[string]any) *session.Event {
	ev := trEvent(partial)
	ev.Actions.StateDelta = delta
	return ev
}

func TestTranslateStateDeltaEscapesKeysAndKeepsNull(t *testing.T) {
	tr := newTranslator(trIDs())
	tr.based = true
	frames, err := tr.translate(trState(false, map[string]any{
		"a/b": 1, "x~y": nil, "temp:skip": true, "plain": "v",
	}))
	if err != nil || len(frames) != 1 || frames[0].Type != aguix.EventStateDelta {
		t.Fatalf("frames = %v, err = %v", frames, err)
	}
	raw, err := json.Marshal(frames[0])
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"STATE_DELTA","delta":[{"op":"add","path":"/a~1b","value":1},` +
		`{"op":"add","path":"/plain","value":"v"},{"op":"add","path":"/x~0y","value":null}]}`
	if string(raw) != want {
		t.Errorf("frame:\n got %s\nwant %s", raw, want)
	}
}

func TestTranslateIgnoresStateOnPartialsAndOnlyTempWrites(t *testing.T) {
	tr := newTranslator(trIDs())
	got := trFeed(t, tr,
		trState(true, map[string]any{"k": 1}),
		trState(false, map[string]any{"temp:k": 1}),
	)
	if len(got) != 0 {
		t.Errorf("frames %v", got)
	}
	if tr.based {
		t.Errorf("a write the client never saw counted as its base")
	}
}

func TestTranslateUnencodableStateFailsWithoutChangingState(t *testing.T) {
	tr := newTranslator(trIDs())
	if _, err := tr.translate(trEvent(true, genai.NewPartFromText("open"))); err != nil {
		t.Fatal(err)
	}
	ev := trState(false, map[string]any{"bad": make(chan int)})
	frames, err := tr.translate(ev)
	if err == nil || !strings.Contains(err.Error(), `state key "bad" cannot be encoded`) || frames != nil {
		t.Fatalf("frames = %v, err = %v", frames, err)
	}
	if tr.based {
		t.Errorf("a failed write counted as the client's base")
	}
	if got := trLines(tr.close()); len(got) != 1 || got[0] != "TEXT_END m1" {
		t.Errorf("close after the failure = %v", got)
	}
}

func TestASeededTempKeyIsNeitherSeededNorEchoed(t *testing.T) {
	llm := &scriptLLM{steps: []step{{text: "Hi."}}}
	a, err := llmagent.New(llmagent.Config{Name: "librarian", Model: llm, Instruction: "Scratch: {temp:scratch?}."})
	if err != nil {
		t.Fatal(err)
	}
	in := input("run-1", text("user", "Hi."))
	in.State = json.RawMessage(`{"temp:scratch":"sent","keep":1}`)
	s := serve(t, mustNew(t, a), in)
	if got := mustJSON(t, s.first("STATE_SNAPSHOT")["snapshot"]); got != `{"keep":1}` {
		t.Errorf("snapshot = %s", got)
	}
	if got := instruction(llm.request(0)); !strings.HasPrefix(got, "Scratch: .\n") {
		t.Errorf("the model saw %q", got)
	}
}

func TestAWriteOnTheResumeRunFollowsTheApprovedResult(t *testing.T) {
	ar := newArchive(t)
	llm := &scriptLLM{steps: []step{{text: "Discarded."}}}
	approval := NewApproval(ar.entries)
	root, err := llmagent.New(llmagent.Config{
		Name: "librarian", Model: llm, Toolsets: []tool.Toolset{approval.Gate(ar.ts), ClientToolset()},
		AfterToolCallbacks: []llmagent.AfterToolCallback{
			func(ctx agent.Context, tl tool.Tool, _, result map[string]any, _ error) (map[string]any, error) {
				return result, ctx.State().Set("last_tool", tl.Name())
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := serve(t, mustNew(t, root, WithApproval(approval)), inbound(t, "approve"))
	wantTypes(t, s, "RUN_STARTED", "TOOL_CALL_RESULT", "STATE_DELTA",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END", "RUN_FINISHED")
	if got := mustJSON(t, s.first("STATE_DELTA")["delta"]); got != `[{"op":"add","path":"/last_tool","value":"discard_book"}]` {
		t.Errorf("delta = %s", got)
	}
}
