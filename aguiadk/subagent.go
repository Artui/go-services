package aguiadk

import (
	"fmt"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
)

// SubagentEventName is the CUSTOM event name a delegated sub-agent's progress
// is sent under: one event when it calls a tool and one when the tool returns.
// The web component draws them on the delegating call's card.
const SubagentEventName = "ag_ui.subagent"

const (
	phaseToolCall   = "tool_call"
	phaseToolResult = "tool_result"
)

// subagentStep is the CUSTOM event's value, the shape django-ag-ui sends.
// DelegationID is the delegating call's id, the one the client already drew a
// card for. Tool.OK is null on a call and set on its result.
type subagentStep struct {
	DelegationID string       `json:"delegationId"`
	Agent        string       `json:"agent"`
	Phase        string       `json:"phase"`
	Status       string       `json:"status"`
	Tool         subagentTool `json:"tool"`
}

type subagentTool struct {
	ToolCallID string `json:"toolCallId"`
	Name       string `json:"name"`
	OK         *bool  `json:"ok"`
}

// progress reports a call c made inside a delegation, that is, by an agent
// that agenttool runs for an outer call. A call of the run's own agents is
// already on the wire as TOOL_CALL_*, so it sends nothing.
//
// It goes out at once rather than after a result: a sub-agent's steps happen
// while the delegating call runs, and are worth seeing only then.
func progress(ctx agent.Context, c *slot, phase string, ok *bool) {
	if c.parent == nil {
		return
	}
	id, name := c.named()
	who := ctx.AgentName()
	status := fmt.Sprintf("%s: calling %s", who, name)
	if ok != nil {
		outcome := "returned"
		if !*ok {
			outcome = "failed"
		}
		status = fmt.Sprintf("%s: %s %s", who, name, outcome)
	}
	c.sink.live(aguix.Custom(SubagentEventName, subagentStep{
		DelegationID: c.parent.root(), Agent: who, Phase: phase, Status: status,
		Tool: subagentTool{ToolCallID: id, Name: name, OK: ok},
	}))
}

// started opens the lifecycle of the sub-agent agenttool runs for ctx's call:
// the first agent to run under a call is the delegated one, and any it hands on
// to belongs to the same invocation. A run's own agents run under no call.
func started(ctx agent.Context) {
	c := slotOf(ctx)
	if c == nil {
		return
	}
	call, _ := c.named()
	c.mu.Lock()
	if c.subagent != "" {
		c.mu.Unlock()
		return
	}
	c.agent, c.subagent = ctx.AgentName(), c.sink.subagentRunID(call)
	name, id := c.agent, c.subagent
	c.mu.Unlock()

	ev := aguix.SubagentStarted(id, name, call)
	if c.parent != nil {
		_, parent := c.parent.delegated()
		ev = ev.WithParentSubagentRunID(parent)
	}
	c.sink.live(ev)
}

// finished closes the lifecycle ctx's call opened, if it opened one. A failure
// carries none of the error's words, which are written for an operator.
func finished(ctx agent.Context, c *slot, ok bool) {
	name, id := c.delegated()
	switch {
	case id == "":
	case ok:
		c.sink.live(aguix.SubagentFinished(id))
	case ctx.Err() != nil:
		c.sink.live(aguix.SubagentError(id, name+" was cancelled").WithCode("cancelled"))
	default:
		c.sink.live(aguix.SubagentError(id, name+" failed"))
	}
}

// subagentRunID names the invocation call makes, "subagent-<call>" as
// django-ag-ui does. A reused call id is numbered, skipping ids already issued,
// since the client refuses a run that starts one sub-agent id twice.
func (s *sink) subagentRunID(call string) string {
	base := "subagent-" + call
	s.mu.Lock()
	defer s.mu.Unlock()
	id := base
	for n := 2; s.issued[id]; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	s.issued[id] = true
	return id
}
