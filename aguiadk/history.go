package aguiadk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

// userAuthor is the author ADK's contents builder treats as the person, not as
// another agent whose turns it rewrites into "For context" text.
const userAuthor = "user"

type turnKind int

const (
	userTurn turnKind = iota
	modelTurn
	toolTurn
)

type turn struct {
	kind    turnKind
	content *genai.Content
}

// callRef is what a tool message needs from the call it answers.
type callRef struct {
	name string
	id   string
}

// rebuild turns an AG-UI conversation into the session events that precede the run
// and the content the run answers.
//
// The tail is the trailing user message, or the trailing run of tool messages
// merged into one content. A conversation that ends on an assistant message, or
// holds nothing ADK can replay, has no tail: rebuild returns a nil tail and no
// error, and every turn it found is history.
//
// Assistant turns are authored by author, the root agent's name, and everything
// else by "user": ADK presents a turn by any other author to the model as
// another agent's context rather than as its own conversation.
func rebuild(messages []aguix.Message, author string) (history []*session.Event, tail *genai.Content, err error) {
	// ADK pairs every call of an id with that id's last result, so a call whose
	// id a later message reuses is renamed in history, together with its result.
	lastCall := map[string]int{}
	for i, m := range messages {
		for _, c := range m.ToolCalls {
			lastCall[c.ID] = i
		}
	}
	calls := map[string]callRef{}
	var (
		turns    []turn
		thoughts []*genai.Part
	)
	for i, m := range messages {
		switch m.Role {
		case "user":
			thoughts = nil
			// Text only: image and other non-text parts are not carried yet.
			turns = append(turns, turn{userTurn, genai.NewContentFromText(m.Text(), genai.RoleUser)})
		case "reasoning":
			// Claude refuses a tool result whose call's turn lost its thinking, so
			// a signed block goes back unchanged, on the model turn it sits in: the
			// client files a block sealed after the text started behind that text.
			if m.EncryptedValue == "" {
				continue
			}
			part := &genai.Part{Thought: true, Text: m.Text(), ThoughtSignature: []byte(m.EncryptedValue)}
			if n := len(turns); n > 0 && turns[n-1].kind == modelTurn {
				turns[n-1].content.Parts = withThoughts(turns[n-1].content.Parts, part)
				continue
			}
			thoughts = append(thoughts, part)
		case "assistant":
			parts, err := assistantParts(m, i, lastCall, calls)
			if err != nil {
				return nil, nil, err
			}
			if len(parts) == 0 {
				continue
			}
			parts = withThoughts(parts, thoughts...)
			thoughts = nil
			// One model turn, however many messages the client split it into:
			// ADK pairs a merged tool result with every call event it answers.
			if n := len(turns); n > 0 && turns[n-1].kind == modelTurn {
				turns[n-1].content.Parts = append(turns[n-1].content.Parts, parts...)
				continue
			}
			turns = append(turns, turn{modelTurn, &genai.Content{Role: genai.RoleModel, Parts: parts}})
		case "tool":
			thoughts = nil
			part, err := toolPart(m, calls)
			if err != nil {
				return nil, nil, err
			}
			// ADK and anthropicmodel would merge these too; merging here makes the
			// trailing run the one content Runner.Run takes.
			if n := len(turns); n > 0 && turns[n-1].kind == toolTurn {
				turns[n-1].content.Parts = append(turns[n-1].content.Parts, part)
				continue
			}
			turns = append(turns, turn{toolTurn, &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{part}}})
		}
		// The agent's Instruction is the system prompt, so a client's system or
		// developer message is dropped, as are activity messages and unsigned or
		// unfollowed reasoning.
	}

	if n := len(turns); n > 0 && turns[n-1].kind != modelTurn {
		tail = turns[n-1].content
		turns = turns[:n-1]
	}
	for _, t := range turns {
		who := userAuthor
		if t.kind == modelTurn {
			who = author
		}
		history = append(history, &session.Event{Author: who, LLMResponse: model.LLMResponse{Content: t.content}})
	}
	return history, tail, nil
}

// withThoughts puts thoughts after the turn's leading thoughts and ahead of the
// rest, the order a provider produced them in.
func withThoughts(parts []*genai.Part, thoughts ...*genai.Part) []*genai.Part {
	at := 0
	for at < len(parts) && parts[at].Thought {
		at++
	}
	return slices.Insert(parts, at, thoughts...)
}

func assistantParts(m aguix.Message, at int, lastCall map[string]int, calls map[string]callRef) ([]*genai.Part, error) {
	var parts []*genai.Part
	if text := m.Text(); text != "" {
		parts = append(parts, genai.NewPartFromText(text))
	}
	for _, call := range m.ToolCalls {
		// Only ADK may ask for a confirmation. A call of that name in the history
		// was written by the client, or is a model's call to a reserved tool name
		// that the bridge streamed as an ordinary call. Either way it is refused.
		if call.Function.Name == toolconfirmation.FunctionCallName {
			return nil, fmt.Errorf("aguiadk: tool call %q names a reserved tool", call.ID)
		}
		args := map[string]any{}
		if call.Function.Arguments != "" {
			decoded, err := decodeObject(call.Function.Arguments)
			if err != nil {
				return nil, fmt.Errorf("aguiadk: arguments of tool call %q: %w", call.ID, err)
			}
			args = decoded
		}
		id := call.ID
		if lastCall[id] != at {
			id = fmt.Sprintf("%s-%d", id, at)
		}
		calls[call.ID] = callRef{name: call.Function.Name, id: id}
		parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{
			ID: id, Name: call.Function.Name, Args: args,
		}})
	}
	return parts, nil
}

func toolPart(m aguix.Message, calls map[string]callRef) (*genai.Part, error) {
	ref, ok := calls[m.ToolCallID]
	if !ok {
		return nil, fmt.Errorf("aguiadk: tool message answers unknown tool call %q", m.ToolCallID)
	}
	response := map[string]any{"error": m.Error}
	if m.Error == "" {
		text := m.Text()
		if decoded, err := decodeObject(text); err == nil {
			response = decoded
		} else {
			response = map[string]any{"result": text}
		}
	}
	return &genai.Part{FunctionResponse: &genai.FunctionResponse{
		ID: ref.id, Name: ref.name, Response: response,
	}}, nil
}

var errNotObject = errors.New("not a JSON object")

// decodeObject keeps numbers as json.Number, so an integer past 2^53 survives.
func decodeObject(text string) (map[string]any, error) {
	if !json.Valid([]byte(text)) {
		return nil, errors.New("not valid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, errNotObject
	}
	return obj, nil
}
