package aguiadk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"math"
	"slices"
	"strings"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolutils"
	"google.golang.org/genai"
)

func trIDs() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("m%d", n) }
}

// trLine renders a frame as one comparable line.
func trLine(e aguix.Event) string {
	switch e.Type {
	case aguix.EventTextMessageStart:
		return "TEXT_START " + e.MessageID
	case aguix.EventTextMessageContent:
		return "TEXT " + e.MessageID + " " + e.Delta
	case aguix.EventTextMessageEnd:
		return "TEXT_END " + e.MessageID
	case aguix.EventToolCallStart:
		return "CALL_START " + e.ToolCallID + " " + e.ToolCallName + " parent=" + e.ParentMessageID
	case aguix.EventToolCallArgs:
		return "CALL_ARGS " + e.ToolCallID + " " + e.Delta
	case aguix.EventToolCallEnd:
		return "CALL_END " + e.ToolCallID
	case aguix.EventToolCallResult:
		return fmt.Sprintf("RESULT %s %s outcome=%q %s", e.MessageID, e.ToolCallID, e.Outcome, e.Content)
	case aguix.EventReasoningStart:
		return "THINK_START " + e.MessageID
	case aguix.EventReasoningMessageStart:
		return "THINK_MSG " + e.MessageID
	case aguix.EventReasoningMessageContent:
		return "THINK " + e.MessageID + " " + e.Delta
	case aguix.EventReasoningMessageEnd:
		return "THINK_MSG_END " + e.MessageID
	case aguix.EventReasoningEnd:
		return "THINK_END " + e.MessageID
	case aguix.EventReasoningEncryptedValue:
		var v sealed
		if err := json.Unmarshal([]byte(e.EncryptedValue), &v); err != nil {
			return "UNREADABLE SEAL " + e.EncryptedValue
		}
		line := "SEAL " + e.Subtype + " " + e.EntityID
		for _, b := range v.Blocks {
			line += fmt.Sprintf(" %s@%d", b.Signature, b.Length)
		}
		return line
	}
	return "UNEXPECTED " + string(e.Type)
}

func trLines(events []aguix.Event) []string {
	lines := make([]string, len(events))
	for i, e := range events {
		lines[i] = trLine(e)
	}
	return lines
}

// trCheckSequence asserts the stream is well formed: text content only inside its
// own START/END pair, call frames in order, a result only after its call ended,
// and nothing left open.
func trCheckSequence(t *testing.T, events []aguix.Event) {
	t.Helper()
	openText, openThought := "", ""
	thoughtState := map[string]string{}
	callState := map[string]string{}
	for i, e := range events {
		if e.Type != aguix.EventReasoningEncryptedValue && strings.HasPrefix(string(e.Type), "REASONING_") {
			trCheckThought(t, i, e, &openThought, openText, thoughtState)
			continue
		}
		switch e.Type {
		case aguix.EventReasoningEncryptedValue:
			if e.Subtype != "message" || thoughtState[e.EntityID] != "ended" {
				t.Errorf("frame %d seals %s %s, which is not a closed reasoning message", i, e.Subtype, e.EntityID)
			}
			thoughtState[e.EntityID] = "sealed"
		case aguix.EventTextMessageStart:
			if openText != "" || openThought != "" {
				t.Errorf("frame %d starts %s while %s%s is open", i, e.MessageID, openText, openThought)
			}
			openText = e.MessageID
		case aguix.EventTextMessageContent:
			if openText != e.MessageID {
				t.Errorf("frame %d: content for %s outside its START/END", i, e.MessageID)
			}
		case aguix.EventTextMessageEnd:
			if openText != e.MessageID {
				t.Errorf("frame %d ends %s, open is %q", i, e.MessageID, openText)
			}
			openText = ""
		case aguix.EventToolCallStart:
			if openText != "" || openThought != "" {
				t.Errorf("frame %d starts a call inside %s%s", i, openText, openThought)
			}
			if callState[e.ToolCallID] != "" {
				t.Errorf("frame %d: call %s announced twice", i, e.ToolCallID)
			}
			callState[e.ToolCallID] = "started"
		case aguix.EventToolCallArgs:
			if callState[e.ToolCallID] != "started" {
				t.Errorf("frame %d: args for %s not right after START", i, e.ToolCallID)
			}
			callState[e.ToolCallID] = "args"
		case aguix.EventToolCallEnd:
			if callState[e.ToolCallID] != "args" {
				t.Errorf("frame %d: END for %s before its args", i, e.ToolCallID)
			}
			callState[e.ToolCallID] = "ended"
		case aguix.EventToolCallResult:
			if callState[e.ToolCallID] != "ended" {
				t.Errorf("frame %d: result for %s before its call ended", i, e.ToolCallID)
			}
			callState[e.ToolCallID] = "answered"
		default:
			t.Errorf("frame %d: unexpected %s", i, e.Type)
		}
	}
	if openText != "" || openThought != "" {
		t.Errorf("%s%s still open at the end", openText, openThought)
	}
	for id, state := range callState {
		if state == "started" || state == "args" {
			t.Errorf("call %s left %s", id, state)
		}
	}
}

// trCheckThought follows one reasoning span: START, MESSAGE_START, content,
// MESSAGE_END, END, never beside open text, and never reopened.
func trCheckThought(t *testing.T, i int, e aguix.Event, open *string, openText string, state map[string]string) {
	t.Helper()
	id, at := e.MessageID, state[e.MessageID]
	want, next := map[aguix.EventType]string{
		aguix.EventReasoningStart:          "",
		aguix.EventReasoningMessageStart:   "span",
		aguix.EventReasoningMessageContent: "message",
		aguix.EventReasoningMessageEnd:     "message",
		aguix.EventReasoningEnd:            "closed",
	}[e.Type], map[aguix.EventType]string{
		aguix.EventReasoningStart:          "span",
		aguix.EventReasoningMessageStart:   "message",
		aguix.EventReasoningMessageContent: "message",
		aguix.EventReasoningMessageEnd:     "closed",
		aguix.EventReasoningEnd:            "ended",
	}[e.Type]
	if at != want || (e.Type == aguix.EventReasoningStart && (*open != "" || openText != "")) {
		t.Errorf("frame %d: %s for %s in state %q (open %q, text %q)", i, e.Type, id, at, *open, openText)
	}
	if e.Type == aguix.EventReasoningMessageContent && e.Delta == "" {
		t.Errorf("frame %d: empty reasoning delta", i)
	}
	state[id] = next
	*open = id
	if next == "ended" {
		*open = ""
	}
}

func trFeed(t *testing.T, tr *translator, events ...*session.Event) []aguix.Event {
	t.Helper()
	var out []aguix.Event
	for _, ev := range events {
		frames, err := tr.translate(ev)
		if err != nil {
			t.Fatalf("translate: %v", err)
		}
		out = append(out, frames...)
	}
	return append(out, tr.close()...)
}

func trEvent(partial bool, parts ...*genai.Part) *session.Event {
	return &session.Event{
		Author: "librarian",
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{Role: genai.RoleModel, Parts: parts}, Partial: partial,
		},
	}
}

func trCall(id, name string, args map[string]any) *genai.Part {
	return &genai.Part{FunctionCall: &genai.FunctionCall{ID: id, Name: name, Args: args}}
}

func trResponse(id, name string, response map[string]any) *session.Event {
	return &session.Event{
		Author: "librarian",
		LLMResponse: model.LLMResponse{Content: &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{ID: id, Name: name, Response: response}},
		}}},
	}
}

func trWant(t *testing.T, got []aguix.Event, want ...string) {
	t.Helper()
	trCheckSequence(t, got)
	if lines := trLines(got); !slices.Equal(lines, want) {
		t.Errorf("frames:\n  %s\nwant:\n  %s", strings.Join(lines, "\n  "), strings.Join(want, "\n  "))
	}
}

// Under SSE the complete event repeats every delta; the client must see them once.
func TestTranslateSendsStreamedTextOnce(t *testing.T) {
	got := trFeed(t, newTranslator(trIDs()),
		trEvent(true, genai.NewPartFromText("Hello")),
		trEvent(true, genai.NewPartFromText(", world")),
		trEvent(false, genai.NewPartFromText("Hello, world")),
	)
	trWant(t, got, "TEXT_START m1", "TEXT m1 Hello", "TEXT m1 , world", "TEXT_END m1")
}

func TestTranslateSendsUnstreamedTextWhole(t *testing.T) {
	got := trFeed(t, newTranslator(trIDs()), trEvent(false, genai.NewPartFromText("Hello, world")))
	trWant(t, got, "TEXT_START m1", "TEXT m1 Hello, world", "TEXT_END m1")
}

// anthropicmodel with an output schema streams thinking but no text, so the
// complete event's text is the only copy.
func TestTranslateThoughtPartialsDoNotSuppressTheFinalText(t *testing.T) {
	thought := &genai.Part{Text: "pondering", Thought: true}
	got := trFeed(t, newTranslator(trIDs()),
		trEvent(true, thought),
		trEvent(true, genai.NewPartFromText("")),
		trEvent(false, thought, genai.NewPartFromText("Answer")),
	)
	trWant(t, got,
		"THINK_START m1", "THINK_MSG m1", "THINK m1 pondering", "THINK_MSG_END m1", "THINK_END m1",
		"TEXT_START m2", "TEXT m2 Answer", "TEXT_END m2")
}

// Each model turn's dedupe is its own: text after a tool result is a new message.
func TestTranslateEachTurnIsItsOwnMessage(t *testing.T) {
	got := trFeed(t, newTranslator(trIDs()),
		trEvent(true, genai.NewPartFromText("One")),
		trEvent(false, genai.NewPartFromText("One")),
		trEvent(false, genai.NewPartFromText("Two")),
	)
	trWant(t, got,
		"TEXT_START m1", "TEXT m1 One", "TEXT_END m1",
		"TEXT_START m2", "TEXT m2 Two", "TEXT_END m2")
}

func TestTranslateEndsTextBeforeACallAndParentsTheCall(t *testing.T) {
	tr := newTranslator(trIDs())
	got := trFeed(t, tr,
		trEvent(true, genai.NewPartFromText("Checking")),
		trEvent(false, genai.NewPartFromText("Checking"),
			trCall("c1", "borrow_book", map[string]any{"book_id": 4}),
			trCall("c2", "ping", nil)),
		trResponse("c1", "borrow_book", map[string]any{"loan_id": 40}),
	)
	trWant(t, got,
		"TEXT_START m1", "TEXT m1 Checking", "TEXT_END m1",
		"CALL_START c1 borrow_book parent=m1", `CALL_ARGS c1 {"book_id":4}`, "CALL_END c1",
		"CALL_START c2 ping parent=m1", "CALL_ARGS c2 {}", "CALL_END c2",
		`RESULT m2 c1 outcome="" {"loan_id":40}`)
}

// A call with no text before it has no parent, rather than one that never started.
func TestTranslateCallWithNoTextHasNoParent(t *testing.T) {
	got := trFeed(t, newTranslator(trIDs()), trEvent(false, trCall("c1", "ping", nil)))
	trWant(t, got, "CALL_START c1 ping parent=", "CALL_ARGS c1 {}", "CALL_END c1")
}

// A step after a tool result that says nothing first is not part of the earlier message.
func TestTranslateACallAfterAResultHasNoParent(t *testing.T) {
	got := trFeed(t, newTranslator(trIDs()),
		trEvent(false, genai.NewPartFromText("Looking"), trCall("c1", "a", nil)),
		trResponse("c1", "a", map[string]any{"ok": true}),
		trEvent(false, trCall("c2", "b", nil)),
	)
	trWant(t, got,
		"TEXT_START m1", "TEXT m1 Looking", "TEXT_END m1",
		"CALL_START c1 a parent=m1", "CALL_ARGS c1 {}", "CALL_END c1",
		`RESULT m2 c1 outcome="" {"ok":true}`,
		"CALL_START c2 b parent=", "CALL_ARGS c2 {}", "CALL_END c2")
}

// gemini's aggregator marks every raw chunk partial, calls included, then
// repeats them in the complete event; only that one is executed.
func TestTranslateIgnoresCallsOnPartials(t *testing.T) {
	partial := trEvent(true, trCall("c1", "ping", nil))
	partial.LongRunningToolIDs = []string{"c1"}
	tr := newTranslator(trIDs())
	got := trFeed(t, tr, partial)
	if len(got) != 0 || len(tr.pending()) != 0 {
		t.Errorf("a partial call produced %v, pending %v", trLines(got), tr.pending())
	}
}

func TestTranslateResultOutcome(t *testing.T) {
	tr := newTranslator(trIDs())
	got := trFeed(t, tr,
		trEvent(false, trCall("c1", "borrow_book", nil), trCall("c2", "ping", nil)),
		trResponse("c1", "borrow_book", map[string]any{"error": "book 13 is reference only"}),
		trResponse("c2", "ping", nil),
	)
	trWant(t, got,
		"CALL_START c1 borrow_book parent=", "CALL_ARGS c1 {}", "CALL_END c1",
		"CALL_START c2 ping parent=", "CALL_ARGS c2 {}", "CALL_END c2",
		`RESULT m1 c1 outcome="failed" {"error":"book 13 is reference only"}`,
		`RESULT m2 c2 outcome="" {}`)
}

func TestTranslateOnlyALoneErrorTextIsAFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		response map[string]any
		want     aguix.ToolOutcome
	}{
		"error text alone":         {map[string]any{"error": "no copy"}, aguix.OutcomeFailed},
		"output with error field":  {map[string]any{"loan": 1, "error": "none"}, aguix.OutcomeSuccess},
		"error that is not text":   {map[string]any{"error": map[string]any{"code": 1}}, aguix.OutcomeSuccess},
		"result without error key": {map[string]any{"result": "ok"}, aguix.OutcomeSuccess},
	} {
		t.Run(name, func(t *testing.T) {
			got := trFeed(t, newTranslator(trIDs()), trResponse("c1", "borrow_book", tc.response))
			if len(got) != 1 || got[0].Outcome != tc.want {
				t.Errorf("got %v, want outcome %q", trLines(got), tc.want)
			}
		})
	}
}

func TestTranslateTracksLongRunningCalls(t *testing.T) {
	tr := newTranslator(trIDs())
	calls := trEvent(false, trCall("c1", "ask", nil), trCall("c2", "ping", nil), trCall("c3", "ask", nil))
	calls.LongRunningToolIDs = []string{"c3", "c1"}
	trFeed(t, tr, calls)
	if got := tr.pending(); !slices.Equal(got, []string{"c1", "c3"}) {
		t.Fatalf("pending = %v, want [c1 c3] in call order", got)
	}
	tr.pending()[0] = "mutated"
	trFeed(t, tr, calls, trResponse("c1", "ask", map[string]any{"answer": "yes"}))
	if got := tr.pending(); !slices.Equal(got, []string{"c3"}) {
		t.Errorf("pending = %v, want [c3] once c1 is answered and repeats ignored", got)
	}
}

func TestTranslateAnErrorCodeAloneFailsTheRun(t *testing.T) {
	tr := newTranslator(trIDs())
	frames, err := tr.translate(&session.Event{LLMResponse: model.LLMResponse{ErrorCode: "SAFETY"}})
	if !errors.Is(err, errModelReported) || frames != nil {
		t.Errorf("err = %v, frames = %v, want errModelReported", err, frames)
	}
}

func TestTranslateModelErrorKeepsTheProviderWordsOffTheError(t *testing.T) {
	tr := newTranslator(trIDs())
	if _, err := tr.translate(trEvent(true, genai.NewPartFromText("Half"))); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []*session.Event{
		{LLMResponse: model.LLMResponse{ErrorCode: "SAFETY", ErrorMessage: "secret provider words"}},
		{LLMResponse: model.LLMResponse{ErrorMessage: "secret provider words"}},
	} {
		frames, err := tr.translate(ev)
		if !errors.Is(err, errModelReported) {
			t.Fatalf("err = %v, want errModelReported", err)
		}
		if strings.Contains(err.Error(), "secret") || frames != nil {
			t.Errorf("err = %q, frames = %v", err, frames)
		}
	}
	if got := trLines(tr.close()); !slices.Equal(got, []string{"TEXT_END m1"}) {
		t.Errorf("close after an error = %v, want the open message ended", got)
	}
}

func TestTranslateUnencodableValuesFailWithoutChangingState(t *testing.T) {
	for name, ev := range map[string]*session.Event{
		"args":     trEvent(false, genai.NewPartFromText("x"), trCall("c1", "f", map[string]any{"v": math.NaN()})),
		"response": trResponse("c1", "f", map[string]any{"v": make(chan int)}),
	} {
		t.Run(name, func(t *testing.T) {
			tr := newTranslator(trIDs())
			frames, err := tr.translate(ev)
			if err == nil || !strings.Contains(err.Error(), "cannot be encoded") || frames != nil {
				t.Fatalf("frames = %v, err = %v", frames, err)
			}
			if got := tr.close(); len(got) != 0 {
				t.Errorf("a failed translate left %v open", trLines(got))
			}
		})
	}
}

func TestTranslateUserEventsAndEmptyPartsProduceNothing(t *testing.T) {
	user := trEvent(false, genai.NewPartFromText("borrow book 4"))
	user.Author = "user"
	empty := &session.Event{Author: "librarian"}
	got := trFeed(t, newTranslator(trIDs()),
		user, empty, trEvent(false, nil, genai.NewPartFromText("")))
	if len(got) != 0 {
		t.Errorf("frames = %v, want none", trLines(got))
	}
}

// A complete event with no content still ends text a stream left open.
func TestTranslateContentlessCompleteEventEndsOpenText(t *testing.T) {
	got := trFeed(t, newTranslator(trIDs()),
		trEvent(true, genai.NewPartFromText("Hi")), &session.Event{Author: "librarian"})
	trWant(t, got, "TEXT_START m1", "TEXT m1 Hi", "TEXT_END m1")
}

type trStep struct {
	text  string
	calls []*genai.FunctionCall
}

// trStreamingLLM answers as anthropicmodel does: under SSE, text partials and
// thinking partials, then one complete response repeating the text with the calls.
type trStreamingLLM struct {
	steps []trStep
	turn  int
}

func (m *trStreamingLLM) Name() string { return "tr-streaming" }

func (m *trStreamingLLM) GenerateContent(
	_ context.Context, _ *model.LLMRequest, stream bool,
) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		step := trStep{text: "That is all."}
		if m.turn < len(m.steps) {
			step = m.steps[m.turn]
		}
		m.turn++
		if stream {
			partial := func(p *genai.Part) bool {
				return yield(&model.LLMResponse{
					Content: &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{p}},
					Partial: true,
				}, nil)
			}
			if !partial(&genai.Part{Text: "thinking", Thought: true}) {
				return
			}
			for _, word := range strings.SplitAfter(step.text, " ") {
				if !partial(genai.NewPartFromText(word)) {
					return
				}
			}
		}
		parts := []*genai.Part{
			{Text: "thinking", Thought: true, ThoughtSignature: []byte("sig-" + step.text[:2])},
			genai.NewPartFromText(step.text),
		}
		for _, c := range step.calls {
			parts = append(parts, &genai.Part{FunctionCall: c})
		}
		yield(&model.LLMResponse{
			Content:      &genai.Content{Role: genai.RoleModel, Parts: parts},
			TurnComplete: true,
		}, nil)
	}
}

// trClientTool is a client-side tool: long-running, and ADK sends no response.
type trClientTool struct{}

func (trClientTool) Name() string        { return "ask_user" }
func (trClientTool) Description() string { return "Ask the person in front of the page." }
func (trClientTool) IsLongRunning() bool { return true }
func (trClientTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "ask_user", Description: "Ask the person."}
}

func (trClientTool) Run(agent.Context, any) (map[string]any, error) { return nil, nil }

func (t trClientTool) ProcessRequest(_ agent.Context, req *model.LLMRequest) error {
	return toolutils.PackTool(req, t)
}

type trBorrowIn struct {
	BookID int `json:"book_id"`
}

func trRun(t *testing.T, mode agent.StreamingMode) ([]aguix.Event, []string) {
	t.Helper()
	reg := services.New(func(context.Context, any) (struct{}, error) { return struct{}{}, nil })
	services.MustRegister(reg, services.Spec[struct{}, trBorrowIn, string]{
		Name: "borrow_book", Kind: services.Query,
		Permit: []func(services.Ctx[struct{}], trBorrowIn) error{
			func(services.Ctx[struct{}], trBorrowIn) error {
				return fmt.Errorf("%w: book 13 is reference only", services.ErrPermission)
			},
		},
		Run: func(services.Ctx[struct{}], trBorrowIn) (string, error) { return "", nil },
	})
	ts, err := adkx.Toolset(reg, adkx.Anonymous)
	if err != nil {
		t.Fatal(err)
	}
	llm := &trStreamingLLM{steps: []trStep{
		{text: "Let me check the shelf.", calls: []*genai.FunctionCall{
			{ID: "c1", Name: "borrow_book", Args: map[string]any{"book_id": 13}}}},
		{text: "It is reference only. Shall I ask?", calls: []*genai.FunctionCall{
			{ID: "c2", Name: "ask_user", Args: map[string]any{"question": "Another?"}}}},
	}}
	a, err := llmagent.New(llmagent.Config{
		Name: "librarian", Model: llm, Tools: []tool.Tool{trClientTool{}}, Toolsets: []tool.Toolset{ts},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := session.InMemoryService()
	r, err := runner.New(runner.Config{AppName: "app", Agent: a, SessionService: svc})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := svc.Create(t.Context(), &session.CreateRequest{AppName: "app", UserID: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	tr := newTranslator(trIDs())
	var out []aguix.Event
	for ev, err := range r.Run(t.Context(), "ada", sess.Session.ID(),
		genai.NewContentFromText("borrow book 13", genai.RoleUser),
		agent.RunConfig{StreamingMode: mode}) {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if ev.Author == userAuthor {
			t.Errorf("the runner yielded the user's own message")
		}
		frames, err := tr.translate(ev)
		if err != nil {
			t.Fatalf("translate: %v", err)
		}
		out = append(out, frames...)
	}
	return append(out, tr.close()...), tr.pending()
}

func TestTranslateARealRunner(t *testing.T) {
	refusal := `RESULT %s c1 outcome="failed" {"error":"permission denied: book 13 is reference only"}`
	call1 := []string{
		"CALL_START c1 borrow_book parent=m2", `CALL_ARGS c1 {"book_id":13}`, "CALL_END c1",
	}
	call2 := func(parent string) []string {
		return []string{
			"CALL_START c2 ask_user parent=" + parent, `CALL_ARGS c2 {"question":"Another?"}`, "CALL_END c2",
		}
	}
	thought := func(id, sig string) []string {
		return []string{"THINK_START " + id, "THINK_MSG " + id, "THINK " + id + " thinking",
			"THINK_MSG_END " + id, "THINK_END " + id, "SEAL message " + id + " " + sig + "@8"}
	}
	// Streamed, the seal follows the span it closes and precedes the text.
	sse := slices.Concat(
		[]string{"THINK_START m1", "THINK_MSG m1", "THINK m1 thinking", "THINK_MSG_END m1", "THINK_END m1",
			"TEXT_START m2", "TEXT m2 Let ", "TEXT m2 me ", "TEXT m2 check ", "TEXT m2 the ",
			"TEXT m2 shelf.", "TEXT_END m2", "SEAL message m1 sig-Le@8"},
		call1, []string{fmt.Sprintf(refusal, "m3")},
		[]string{"THINK_START m4", "THINK_MSG m4", "THINK m4 thinking", "THINK_MSG_END m4", "THINK_END m4",
			"TEXT_START m5", "TEXT m5 It ", "TEXT m5 is ", "TEXT m5 reference ", "TEXT m5 only. ",
			"TEXT m5 Shall ", "TEXT m5 I ", "TEXT m5 ask?", "TEXT_END m5", "SEAL message m4 sig-It@8"},
		call2("m5"),
	)
	whole := slices.Concat(
		thought("m1", "sig-Le"),
		[]string{"TEXT_START m2", "TEXT m2 Let me check the shelf.", "TEXT_END m2"},
		call1, []string{fmt.Sprintf(refusal, "m3")},
		thought("m4", "sig-It"),
		[]string{"TEXT_START m5", "TEXT m5 It is reference only. Shall I ask?", "TEXT_END m5"},
		call2("m5"),
	)
	for name, tc := range map[string]struct {
		mode agent.StreamingMode
		want []string
	}{
		"sse":  {agent.StreamingModeSSE, sse},
		"none": {agent.StreamingModeNone, whole},
	} {
		t.Run(name, func(t *testing.T) {
			got, pending := trRun(t, tc.mode)
			trWant(t, got, tc.want...)
			if !slices.Equal(pending, []string{"c2"}) {
				t.Errorf("pending = %v, want [c2]", pending)
			}
		})
	}
}
