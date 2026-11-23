package aguiadk

import (
	"fmt"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

// openCall is a call of the conversation that no tool message has answered.
type openCall struct {
	name, args string
	at         int
}

// resumed rebuilds the state ADK resumes a confirmed call from: after the
// history, one event by the root agent asking to confirm each answered call,
// then a tail holding only the answers. ADK drops from the model's view any
// content with a confirmation part, so a trailing tool result the client sent
// moves into the history as its own turn.
//
// Every entry must name, through its interrupt id, a call of the conversation
// that is still unanswered and that no user message follows; anything else is
// refused, so a resume sent twice cannot run a call twice. It returns the calls
// the person denied, whose results are then reported as denied.
func resumed(
	messages []aguix.Message, entries []aguix.ResumeEntry,
	history []*session.Event, tail *genai.Content, author string,
) ([]*session.Event, *genai.Content, map[string]bool, error) {
	open := map[string]openCall{}
	lastUser := -1
	for i, m := range messages {
		switch m.Role {
		case "user":
			lastUser = i
		case "assistant":
			for _, c := range m.ToolCalls {
				open[c.ID] = openCall{name: c.Function.Name, args: c.Function.Arguments, at: i}
			}
		case "tool":
			delete(open, m.ToolCallID)
		}
	}

	var (
		calls   []*genai.Part
		answers []*genai.Part
		ids     []string
		denied  = map[string]bool{}
		seen    = map[string]bool{}
	)
	for _, entry := range entries {
		d, err := decide(entry)
		if err != nil {
			return nil, nil, nil, err
		}
		c, ok := open[d.callID]
		if !ok || c.at < lastUser || seen[d.callID] {
			return nil, nil, nil, fmt.Errorf(
				"aguiadk: interrupt %q answers no call the conversation left waiting", entry.InterruptID)
		}
		seen[d.callID] = true
		args := d.edited
		if args == nil {
			args = map[string]any{}
			if c.args != "" {
				// rebuild has already decoded these arguments without error.
				args, _ = decodeObject(c.args)
			}
		}
		if !d.approved {
			denied[d.callID] = true
		}
		calls = append(calls, &genai.Part{FunctionCall: &genai.FunctionCall{
			ID: entry.InterruptID, Name: toolconfirmation.FunctionCallName,
			Args: map[string]any{"originalFunctionCall": &genai.FunctionCall{
				ID: d.callID, Name: c.name, Args: args,
			}},
		}})
		answers = append(answers, &genai.Part{FunctionResponse: &genai.FunctionResponse{
			ID: entry.InterruptID, Name: toolconfirmation.FunctionCallName,
			Response: map[string]any{"confirmed": d.approved},
		}})
		ids = append(ids, entry.InterruptID)
	}

	if tail != nil {
		history = append(history, &session.Event{Author: userAuthor, LLMResponse: model.LLMResponse{Content: tail}})
	}
	history = append(history, &session.Event{
		Author:             author,
		LongRunningToolIDs: ids,
		LLMResponse:        model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel, Parts: calls}},
	})
	return history, &genai.Content{Role: genai.RoleUser, Parts: answers}, denied, nil
}
