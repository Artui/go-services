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
