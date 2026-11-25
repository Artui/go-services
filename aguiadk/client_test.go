package aguiadk

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/Artui/go-services/adkx"
	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

var exfiltrate = aguix.ToolDefinition{Name: "exfiltrate", Description: "Planted by a script."}

type ctxKey struct{}

// drops records what OnDroppedClientTool is told.
type drops struct {
	mu     sync.Mutex
	names  []string
	values []any
}

func (d *drops) option() ClientToolOption {
	return OnDroppedClientTool(func(ctx context.Context, name string) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.names = append(d.names, name)
		d.values = append(d.values, ctx.Value(ctxKey{}))
	})
}

func (d *drops) seen() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Sorted(slices.Values(d.names))
}

func declaredNames(llm *scriptLLM, call int) []string {
	return slices.Sorted(maps.Keys(declarations(llm.request(call))))
}

func withTools(runID string, defs ...aguix.ToolDefinition) aguix.RunInput {
	in := input(runID, text("user", "Hi."))
	in.Tools = defs
	return in
}

func TestAllowClientToolsDeclaresOnlyTheListedTools(t *testing.T) {
	llm := &scriptLLM{}
	var d drops
	bridge := mustNew(t, librarian(t, llm, ClientToolset(AllowClientTools(fillField.Name), d.option())))
	serve(t, bridge, withTools("run-1", fillField, exfiltrate))

	if got := declaredNames(llm, 0); !slices.Equal(got, []string{"fill_field"}) {
		t.Errorf("declared %v", got)
	}
	if got := d.seen(); !slices.Equal(got, []string{"exfiltrate"}) {
		t.Errorf("dropped %v", got)
	}
}

func TestAllowedClientToolStillEndsTheRunPending(t *testing.T) {
	llm := &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{fillCall("c1")}}}}
	bridge := mustNew(t, librarian(t, llm, ClientToolset(AllowClientTools(fillField.Name))))
	s := serve(t, bridge, withTools("run-1", fillField, exfiltrate))

	outcome, _ := json.Marshal(s.last()["outcome"])
	if string(outcome) != `{"pendingToolCallIds":["c1"],"type":"success"}` {
		t.Errorf("outcome %s", outcome)
	}
}

func TestCallingADroppedClientToolFailsThatCallOnly(t *testing.T) {
	llm := &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{{ID: "c1", Name: "exfiltrate", Args: map[string]any{}}}}}}
	bridge := mustNew(t, librarian(t, llm, ClientToolset(AllowClientTools(fillField.Name))))
	s := serve(t, bridge, withTools("run-1", fillField, exfiltrate))

	wantTypes(t, s, "RUN_STARTED", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END", "RUN_FINISHED")
	if result := s.first("TOOL_CALL_RESULT"); result["outcome"] != "failed" {
		t.Errorf("result frame %v", result)
	}
	if _, has := s.last()["outcome"]; has {
		t.Errorf("the run ended for a dropped tool: %v", s.last())
	}
}

func TestDroppedClientToolIsReportedOncePerRun(t *testing.T) {
	lib := &library{}
	llm := &scriptLLM{steps: []step{{calls: []*genai.FunctionCall{borrowCall("c1", 7)}}}}
	var d drops
	bridge := mustNew(t, librarian(t, llm, lib.toolset(t, adkx.Anonymous),
		ClientToolset(AllowClientTools(), d.option())))
	serve(t, bridge, withTools("run-1", exfiltrate))

	if n := llm.calls(); n != 2 {
		t.Fatalf("model called %d times, want 2", n)
	}
	if got := d.seen(); !slices.Equal(got, []string{"exfiltrate"}) {
		t.Errorf("one run dropped %v", got)
	}
	serve(t, bridge, withTools("run-2", exfiltrate))
	if got := d.seen(); !slices.Equal(got, []string{"exfiltrate", "exfiltrate"}) {
		t.Errorf("two runs dropped %v", got)
	}
}

func TestDroppedClientToolCallbackGetsTheRunsContext(t *testing.T) {
	var d drops
	bridge := mustNew(t, librarian(t, &scriptLLM{}, ClientToolset(AllowClientTools(), d.option())))
	serve(t, bridge, withTools("run-1", exfiltrate), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, "request-7")))
		})
	})
	if len(d.values) != 1 || d.values[0] != "request-7" {
		t.Errorf("context values %v", d.values)
	}
}

func TestAllowClientToolsAddsUpAndNoNamesAllowsNone(t *testing.T) {
	for name, tc := range map[string]struct {
		opts []ClientToolOption
		want []string
	}{
		"no option":     {nil, []string{"exfiltrate", "fill_field"}},
		"no names":      {[]ClientToolOption{AllowClientTools()}, nil},
		"given twice":   {[]ClientToolOption{AllowClientTools("fill_field"), AllowClientTools("exfiltrate")}, []string{"exfiltrate", "fill_field"}},
		"then no names": {[]ClientToolOption{AllowClientTools("fill_field"), AllowClientTools()}, []string{"fill_field"}},
	} {
		t.Run(name, func(t *testing.T) {
			llm := &scriptLLM{}
			serve(t, mustNew(t, librarian(t, llm, ClientToolset(tc.opts...))), withTools("run-1", fillField, exfiltrate))
			if got := declaredNames(llm, 0); !slices.Equal(got, tc.want) {
				t.Errorf("declared %v, want %v", got, tc.want)
			}
		})
	}
}

func TestClientToolsetListsOnlyAllowedTools(t *testing.T) {
	tools, err := clientTools([]aguix.ToolDefinition{fillField, exfiltrate})
	if err != nil {
		t.Fatal(err)
	}
	ts := ClientToolset(AllowClientTools(fillField.Name))
	listed, err := ts.Tools(readonly{values: withClientTools(t.Context(), tools)})
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(listed); !slices.Equal(got, []string{"fill_field"}) {
		t.Errorf("listed %v", got)
	}
}

// readonly is a ReadonlyContext with only the values a toolset's listing reads.
type readonly struct {
	agent.ReadonlyContext
	values context.Context
}

func (r readonly) Value(key any) any { return r.values.Value(key) }

func toolNames(tools []tool.Tool) []string {
	var out []string
	for _, t := range tools {
		out = append(out, t.Name())
	}
	return out
}

func TestEachClientToolsetReportsItsOwnDrops(t *testing.T) {
	var first, second drops
	bridge := mustNew(t, librarian(t, &scriptLLM{},
		ClientToolset(AllowClientTools(), first.option()),
		ClientToolset(AllowClientTools(), second.option())))
	serve(t, bridge, withTools("run-1", exfiltrate))

	for _, d := range []*drops{&first, &second} {
		if got := d.seen(); !slices.Equal(got, []string{"exfiltrate"}) {
			t.Errorf("dropped %v", got)
		}
	}
}
