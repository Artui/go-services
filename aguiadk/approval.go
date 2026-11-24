package aguiadk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

// interruptPrefix makes an interrupt id from the id of the call it gates, as
// pydantic-ai's AG-UI adapter does.
const interruptPrefix = "int-"

// confirmKey is the interrupt metadata key the web component reads its prompt from.
const confirmKey = "x-confirm"

// The web component restores a waiting thread only for an interrupt whose
// metadata kind is "approval", and approves one in place from its inbox only
// when destructive is false; a missing flag counts as destructive.
const (
	kindKey        = "kind"
	kindApproval   = "approval"
	destructiveKey = "destructive"
)

// Approval decides which server tools wait for a person, and carries each spec's
// prompt to the client.
//
// A tool is gated when its spec is Destructive, or Require names it, unless
// Exempt names it. Build one from the registry's entries, gate the toolset with
// Gate, and hand the same value to New with WithApproval:
//
//	approval := aguiadk.NewApproval(reg.Entries(), aguiadk.Exempt("archive_note"))
//	root, _ := llmagent.New(llmagent.Config{
//	    Toolsets: []tool.Toolset{approval.Gate(specs), aguiadk.ClientToolset()},
//	})
//	served, _ := aguiadk.New(root, aguiadk.WithUserID(userID), aguiadk.WithApproval(approval))
//
// A gated call ends the run with an interrupt, and the client's answer arrives
// with the next run: approved, the call runs then, with the arguments the
// person edited if they did; denied, it never runs. The approval is a person's
// confirmation and not an authorization, since the client owns the history it
// sends: the kernel's permission layer stays the boundary.
//
// Gating uses ADK's tool.WithConfirmation, which ADK marks experimental. A gated
// tool must belong to the root agent, because a run resumes there.
type Approval struct {
	gated   map[string]bool
	prompts map[string]string
	// safe names the specs that do not declare themselves destructive.
	safe map[string]bool
}

// ApprovalOption adjusts which tools an Approval gates.
type ApprovalOption func(*approvalLists)

type approvalLists struct {
	require, exempt []string
}

// Require gates the named tools whatever their specs declare, including tools
// that are not specs at all.
func Require(names ...string) ApprovalOption {
	return func(l *approvalLists) { l.require = append(l.require, names...) }
}

// Exempt never gates the named tools. It wins over Require and over Destructive.
func Exempt(names ...string) ApprovalOption {
	return func(l *approvalLists) { l.exempt = append(l.exempt, names...) }
}

// NewApproval gates every entry whose Destructive is set and true. An entry's
// Metadata["x-confirm"], when a non-empty string, is the question the client
// shows for its calls.
func NewApproval(entries []services.Entry, opts ...ApprovalOption) *Approval {
	var lists approvalLists
	for _, opt := range opts {
		opt(&lists)
	}
	a := &Approval{gated: map[string]bool{}, prompts: map[string]string{}, safe: map[string]bool{}}
	for _, e := range entries {
		if e.Destructive != nil && *e.Destructive {
			a.gated[e.Name] = true
		} else {
			a.safe[e.Name] = true
		}
		if prompt, ok := e.Metadata[confirmKey].(string); ok && strings.TrimSpace(prompt) != "" {
			a.prompts[e.Name] = prompt
		}
	}
	for _, name := range lists.require {
		a.gated[name] = true
	}
	for _, name := range lists.exempt {
		delete(a.gated, name)
	}
	return a
}

// Gate returns ts with its gated tools waiting for approval.
func (a *Approval) Gate(ts tool.Toolset) tool.Toolset {
	return tool.WithConfirmation(ts, false, func(name string, _ any) bool { return a.gated[name] })
}

// WithApproval lets the bridge put each gated spec's prompt on its interrupt.
//
// Without it, a call some tool.WithConfirmation gates still ends the run on an
// interrupt, with the generic message only.
func WithApproval(a *Approval) Option {
	return func(b *bridge) {
		b.approval = a
		if a == nil {
			b.optErr = errors.New("aguiadk: WithApproval needs an approval")
		}
	}
}

// destructive reports whether a call to name counts as destructive. A call a's
// own gate asks about does, as django-ag-ui marks it; any other is safe only for
// a spec a knows does not declare itself destructive, since the web component
// approves a safe call in one click and counts an unknown one as destructive.
func (a *Approval) destructive(name string) bool {
	return a == nil || a.gated[name] || !a.safe[name]
}

func (a *Approval) prompt(name string) (string, bool) {
	if a == nil {
		return "", false
	}
	p, ok := a.prompts[name]
	return p, ok
}

// responseSchema is the payload the client answers an interrupt with. A reason
// is not offered: ADK's rejection cannot carry one to the model.
func responseSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"approved":   map[string]any{"type": "boolean"},
			"editedArgs": map[string]any{"type": "object"},
		},
		"required": []any{"approved"},
	}
}

// interruptFor turns one adk_request_confirmation call into the interrupt the
// client answers.
func (a *Approval) interruptFor(confirmation *genai.FunctionCall) (aguix.Interrupt, error) {
	original, err := toolconfirmation.OriginalCallFrom(confirmation)
	if err != nil {
		return aguix.Interrupt{}, fmt.Errorf("aguiadk: a confirmation request: %w", err)
	}
	args := original.Args
	if args == nil {
		args = map[string]any{}
	}
	// encodeParts has already encoded the request's arguments, these among them.
	encoded, _ := json.Marshal(args)
	in := aguix.Interrupt{
		ID:             interruptPrefix + original.ID,
		Reason:         "tool_call",
		ToolCallID:     original.ID,
		Message:        fmt.Sprintf("Approve %s(%s)?", original.Name, encoded),
		ResponseSchema: responseSchema(),
	}
	metadata := map[string]any{kindKey: kindApproval, destructiveKey: a.destructive(original.Name)}
	if prompt, ok := a.prompt(original.Name); ok {
		metadata[confirmKey] = prompt
	}
	in.Metadata = metadata
	return in, nil
}

// decision is the client's answer to one interrupt.
type decision struct {
	callID   string
	approved bool
	edited   map[string]any
}

// decide reads a resume entry as pydantic-ai does: only status "resolved" with
// approved exactly true approves, and edits that are not an object deny.
func decide(entry aguix.ResumeEntry) (decision, error) {
	callID, ok := strings.CutPrefix(entry.InterruptID, interruptPrefix)
	if !ok || callID == "" {
		return decision{}, fmt.Errorf("aguiadk: interrupt %q was not raised by this bridge", entry.InterruptID)
	}
	d := decision{callID: callID}
	if entry.Status != "resolved" {
		return d, nil
	}
	var payload struct {
		Approved   json.RawMessage `json:"approved"`
		EditedArgs json.RawMessage `json:"editedArgs"`
	}
	if json.Unmarshal(entry.Payload, &payload) != nil || !bytes.Equal(payload.Approved, []byte("true")) {
		return d, nil
	}
	if raw := bytes.TrimSpace(payload.EditedArgs); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		edited, err := decodeObject(string(raw))
		if err != nil {
			return d, nil
		}
		d.edited = edited
	}
	d.approved = true
	return d, nil
}
