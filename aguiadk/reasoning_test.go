package aguiadk

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func thought(text, signature string) *genai.Part {
	p := &genai.Part{Thought: true, Text: text}
	if signature != "" {
		p.ThoughtSignature = []byte(signature)
	}
	return p
}

func thinkLines(id, text string) []string {
	lines := []string{"THINK_START " + id, "THINK_MSG " + id}
	if text != "" {
		lines = append(lines, "THINK "+id+" "+text)
	}
	return append(lines, "THINK_MSG_END "+id, "THINK_END "+id)
}

func TestTranslateStreamedThinkingIsSealedOnTheMessageThatHoldsIt(t *testing.T) {
	got := trFeed(t, newTranslator(trIDs()),
		trEvent(true, thought("Check ", "")),
		trEvent(true, thought("the shelf.", "")),
		trEvent(true, genai.NewPartFromText("On it.")),
		trEvent(false, thought("Check the shelf.", "sig-1"), genai.NewPartFromText("On it.")),
	)
	trWant(t, got,
		"THINK_START m1", "THINK_MSG m1", "THINK m1 Check ", "THINK m1 the shelf.",
		"THINK_MSG_END m1", "THINK_END m1",
		"TEXT_START m2", "TEXT m2 On it.", "TEXT_END m2",
		"SEAL message m1 sig-1")
}

func TestTranslateUnstreamedThoughtsAreWholeSpansInOrder(t *testing.T) {
	got := trFeed(t, newTranslator(trIDs()), trEvent(false,
		thought("Plan.", "sig-1"),
		thought("", "redacted-blob"),
		thought("", ""),
		thought("Unsigned.", ""),
		genai.NewPartFromText("Answer"),
	))
	trWant(t, got, slices.Concat(
		thinkLines("m1", "Plan."), []string{"SEAL message m1 sig-1"},
		thinkLines("m2", ""), []string{"SEAL message m2 redacted-blob"},
		thinkLines("m3", "Unsigned."),
		[]string{"TEXT_START m4", "TEXT m4 Answer", "TEXT_END m4"},
	)...)
}

// The blocks after the streamed one did not stream, so each is its own span.
func TestTranslateBlocksAfterAStreamedOneAreSpansOfTheirOwn(t *testing.T) {
	got := trFeed(t, newTranslator(trIDs()),
		trEvent(true, thought("Plan.", "")),
		trEvent(false, thought("Plan.", "sig-1"), thought("", "redacted-blob"), thought("More.", "sig-3")),
	)
	trWant(t, got, slices.Concat(
		thinkLines("m1", "Plan."), []string{"SEAL message m1 sig-1"},
		thinkLines("m2", ""), []string{"SEAL message m2 redacted-blob"},
		thinkLines("m3", "More."), []string{"SEAL message m3 sig-3"},
	)...)
}

// Every shape the stream cannot attribute to one block seals nothing and sends
// nothing more: the streamed text stays, and the next run replays no thinking.
func TestTranslateSealsNothingItCannotAttribute(t *testing.T) {
	for name, events := range map[string][]feedEvent{
		"text differs": {
			{true, []*genai.Part{thought("Plan", "")}},
			{false, []*genai.Part{thought("Plan.", "sig-1")}},
		},
		"two text blocks": {
			{true, []*genai.Part{thought("AB", "")}},
			{false, []*genai.Part{thought("A", "sig-1"), thought("B", "sig-2")}},
		},
		"redacted first": {
			{true, []*genai.Part{thought("Plan.", "")}},
			{false, []*genai.Part{thought("", "redacted-blob"), thought("Plan.", "sig-1")}},
		},
		// Sending B as a span would show it twice.
		"two streamed messages": {
			{true, []*genai.Part{thought("A", "")}},
			{true, []*genai.Part{genai.NewPartFromText("x")}},
			{true, []*genai.Part{thought("B", "")}},
			{false, []*genai.Part{thought("A", "sig-1"), genai.NewPartFromText("x"), thought("B", "sig-2")}},
		},
		"no thought in the complete event": {
			{true, []*genai.Part{thought("Plan.", "")}},
			{false, []*genai.Part{genai.NewPartFromText("Done.")}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var feed []*session.Event
			for _, e := range events {
				feed = append(feed, trEvent(e.partial, e.parts...))
			}
			got := trFeed(t, newTranslator(trIDs()), feed...)
			trCheckSequence(t, got)
			for _, e := range got {
				if e.Type == aguix.EventReasoningEncryptedValue {
					t.Errorf("sealed %s", trLine(e))
				}
			}
			if starts := countType(got, aguix.EventReasoningStart); starts != countPartialThoughtRuns(events) {
				t.Errorf("%d reasoning spans, want only the streamed ones:\n%v", starts, trLines(got))
			}
		})
	}
}

// A signature that is not UTF-8 would be rewritten by JSON, so it is not sent.
func TestTranslateDoesNotSealASignatureJSONWouldRewrite(t *testing.T) {
	got := trFeed(t, newTranslator(trIDs()), trEvent(false, thought("Plan.", "\xff\xfe"), thought("", "\xff")))
	trWant(t, got, thinkLines("m1", "Plan.")...)
}

func TestTranslateCloseEndsAnOpenReasoningSpan(t *testing.T) {
	got := trFeed(t, newTranslator(trIDs()), trEvent(true, thought("Hmm", "")))
	trWant(t, got, thinkLines("m1", "Hmm")...)
}

func TestTranslateThinkingAfterTextEndsTheText(t *testing.T) {
	got := trFeed(t, newTranslator(trIDs()),
		trEvent(true, genai.NewPartFromText("One")),
		trEvent(true, thought("Hmm", "")),
	)
	trWant(t, got, slices.Concat(
		[]string{"TEXT_START m1", "TEXT m1 One", "TEXT_END m1"}, thinkLines("m2", "Hmm"))...)
}

// The reasoning message is never a call's parent: that stays the step's text.
func TestTranslateACallAfterThinkingHasTheTextAsItsParent(t *testing.T) {
	got := trFeed(t, newTranslator(trIDs()), trEvent(false,
		thought("Plan.", ""), genai.NewPartFromText("On it."), trCall("c1", "fill_field", nil)))
	trWant(t, got, slices.Concat(thinkLines("m1", "Plan."),
		[]string{"TEXT_START m2", "TEXT m2 On it.", "TEXT_END m2",
			"CALL_START c1 fill_field parent=m2", "CALL_ARGS c1 {}", "CALL_END c1"})...)
}

type feedEvent struct {
	partial bool
	parts   []*genai.Part
}

func countType(events []aguix.Event, typ aguix.EventType) int {
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// countPartialThoughtRuns counts the reasoning messages a feed's partials open.
func countPartialThoughtRuns(events []feedEvent) int {
	n, open := 0, false
	for _, e := range events {
		if !e.partial {
			open = false
			continue
		}
		for _, p := range e.parts {
			switch {
			case p.Thought && !open:
				n, open = n+1, true
			case !p.Thought:
				open = false
			}
		}
	}
	return n
}

// The blocks a model thinks before a call, as anthropicmodel returns them: one
// signed thinking block, then one redacted block whose data is its signature.
func thinking() []*genai.Part {
	return []*genai.Part{
		thought("The email field is the one to fill.", "EqQBCkYIBxgCKkDsig=="),
		thought("", "EmwKAhgBEgy3redacted=="),
	}
}

// wantThinkingBeforeTheCall asserts the model turn that made call id carries
// thinking() first, unchanged, which is what Claude accepts on its tool result.
func wantThinkingBeforeTheCall(t *testing.T, contents []*genai.Content, id string) {
	t.Helper()
	for _, c := range contents {
		at := slices.IndexFunc(c.Parts, func(p *genai.Part) bool {
			return p.FunctionCall != nil && p.FunctionCall.ID == id
		})
		if at < 0 {
			continue
		}
		want := thinking()
		if len(c.Parts) < len(want) {
			t.Fatalf("the call's turn has %d parts", len(c.Parts))
		}
		for i, w := range want {
			got := c.Parts[i]
			if !got.Thought || got.Text != w.Text || !bytes.Equal(got.ThoughtSignature, w.ThoughtSignature) {
				t.Errorf("part %d = thought %v %q %q, want %q %q",
					i, got.Thought, got.Text, got.ThoughtSignature, w.Text, w.ThoughtSignature)
			}
		}
		return
	}
	t.Fatalf("no model turn made call %s", id)
}

func TestThinkingStreamsAndReturnsOnTheToolResultTurn(t *testing.T) {
	llm := &scriptLLM{steps: []step{
		{thoughts: thinking(), text: "Filling it in.", calls: []*genai.FunctionCall{fillCall("c1")}},
		{text: "Done."},
	}}
	bridge := mustNew(t, librarian(t, llm, ClientToolset()))
	first := input("run-think", text("user", "Fill in my email."))
	first.Tools = []aguix.ToolDefinition{fillField}
	s := serve(t, bridge, first)
	// One delta per word of the thinking block's eight.
	wantTypes(t, s, slices.Concat([]string{"RUN_STARTED", "REASONING_START", "REASONING_MESSAGE_START"},
		slices.Repeat([]string{"REASONING_MESSAGE_CONTENT"}, 8), []string{
			"REASONING_MESSAGE_END", "REASONING_END",
			"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_CONTENT",
			"TEXT_MESSAGE_END", "REASONING_ENCRYPTED_VALUE",
			"REASONING_START", "REASONING_MESSAGE_START", "REASONING_MESSAGE_END", "REASONING_END",
			"REASONING_ENCRYPTED_VALUE",
			"TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "RUN_FINISHED"})...)
	golden(t, "reasoning-pending", s)

	// The client's next request, built from those frames and the page's answer.
	s = serve(t, bridge, inbound(t, "reasoning-tool-result"))
	if s.last()["type"] != "RUN_FINISHED" {
		t.Fatalf("the answering run failed:\n%s", s.body)
	}
	wantThinkingBeforeTheCall(t, llm.request(1).Contents, "c1")
}

func TestThinkingReturnsOnAResumedApproval(t *testing.T) {
	a := newArchive(t)
	llm := &scriptLLM{steps: []step{
		{thoughts: thinking(), calls: []*genai.FunctionCall{discardCall("c1", 7)}},
		{text: "Discarded."},
	}}
	s := serve(t, gatedBridge(t, llm, a), input("run-pause", text("user", "Discard book 7.")))
	if o := outcome(t, s); o["type"] != "interrupt" {
		t.Fatalf("outcome %v", o)
	}
	golden(t, "reasoning-approval-pause", s)

	s = serve(t, gatedBridge(t, llm, a), inbound(t, "reasoning-approve"))
	if ran := a.ran(); !slices.Equal(ran, []string{"discard 7"}) {
		t.Fatalf("ran %v:\n%s", ran, s.body)
	}
	wantThinkingBeforeTheCall(t, llm.request(1).Contents, "c1")
}

// Sealed thinking joins the model turn it sits in, before or after the
// assistant message, ahead of the turn's other parts; thinking with no seal, or
// with a user or tool message before the next model turn, is not replayed.
func TestRebuildPlacesSealedThinkingOnTheModelTurnItSitsIn(t *testing.T) {
	sealed := aguix.Message{ID: "r1", Role: "reasoning", Content: aguix.TextContent("Plan."), EncryptedValue: "sig-1"}
	history, tail, err := rebuild([]aguix.Message{
		text("user", "go"),
		sealed,
		{ID: "a0", Role: "assistant"},
		text("assistant", "On it."),
		{ID: "a2", Role: "assistant", ToolCalls: []aguix.ToolCall{{ID: "c1", Type: "function",
			Function: aguix.FunctionCall{Name: "fill_field", Arguments: "{}"}}}},
		{ID: "a3", Role: "assistant", ToolCalls: []aguix.ToolCall{{ID: "c2", Type: "function",
			Function: aguix.FunctionCall{Name: "fill_field", Arguments: "{}"}}}},
		{ID: "t1", Role: "tool", ToolCallID: "c1", Content: aguix.TextContent("ok")},
		sealed,
		{ID: "t2", Role: "tool", ToolCallID: "c2", Content: aguix.TextContent("ok")},
		text("assistant", "Both done."),
		sealed,
		text("user", "again"),
		{ID: "r3", Role: "reasoning", Content: aguix.TextContent("Unsealed.")},
		text("assistant", "Sure."),
		sealed,
	}, "librarian")
	if err != nil || tail != nil {
		t.Fatalf("rebuild: tail %v, %v", tail, err)
	}
	var got []string
	for _, ev := range history {
		for _, p := range ev.Content.Parts {
			switch {
			case p.Thought:
				got = append(got, "thought "+p.Text+" "+string(p.ThoughtSignature))
			case p.FunctionCall != nil:
				got = append(got, "call "+p.FunctionCall.ID)
			case p.FunctionResponse != nil:
				got = append(got, "response "+p.FunctionResponse.ID)
			default:
				got = append(got, "text "+p.Text)
			}
		}
	}
	want := []string{"text go", "thought Plan. sig-1", "text On it.", "call c1", "call c2",
		"response c1", "response c2", "thought Plan. sig-1", "text Both done.",
		"text again", "thought Plan. sig-1", "text Sure."}
	if !slices.Equal(got, want) {
		t.Errorf("history parts:\n got %v\nwant %v", got, want)
	}
}

// A model that fails while thinking leaves a closed span ahead of RUN_ERROR.
func TestModelFailureMidThoughtClosesTheReasoningFirst(t *testing.T) {
	boom := errors.New("stream broke")
	llm := &scriptLLM{steps: []step{{thoughts: []*genai.Part{thought("Let me see", "")}, err: boom}}}
	s := serve(t, mustNew(t, librarian(t, llm)), input("run-1", text("user", "Think.")))
	wantTypes(t, s, "RUN_STARTED", "REASONING_START", "REASONING_MESSAGE_START",
		"REASONING_MESSAGE_CONTENT", "REASONING_MESSAGE_CONTENT", "REASONING_MESSAGE_CONTENT",
		"REASONING_MESSAGE_END", "REASONING_END", "RUN_ERROR")
	wantRunError(t, s, boom.Error())
	golden(t, "reasoning-model-error", s)
}
