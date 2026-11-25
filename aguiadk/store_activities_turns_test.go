package aguiadk

import (
	"encoding/json"
	"slices"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"github.com/Artui/go-services/aguix"
	"google.golang.org/genai"
)

type suggestIn struct {
	Prompt string `json:"prompt"`
}

// clientSends is the stored thread as @ag-ui/client 1.0.4 sends it back: without its activities.
func clientSends(stored []aguix.Message, next aguix.Message) []aguix.Message {
	return append(slices.DeleteFunc(slices.Clone(stored), func(m aguix.Message) bool { return m.Role == "activity" }), next)
}

func TestAStoredActivityOutlivesTheTurnsAfterIt(t *testing.T) {
	reg := services.New[struct{}](nil)
	services.MustRegister(reg, services.Spec[struct{}, suggestIn, suggestIn]{
		Name: "suggest", Description: "Suggest a next prompt.", Kind: services.Query,
		Run: func(c services.Ctx[struct{}], in suggestIn) (suggestIn, error) {
			_, err := PublishSuggestions(c.Context, "next", in.Prompt)
			return in, err
		},
	})
	ts, err := adkx.Toolset(reg, adkx.Anonymous)
	if err != nil {
		t.Fatal(err)
	}
	suggest := func(id, prompt string) step {
		return step{calls: []*genai.FunctionCall{{ID: id, Name: "suggest", Args: map[string]any{"prompt": prompt}}}}
	}
	llm := &scriptLLM{steps: []step{suggest("c1", "A"), {text: "One."}, {text: "Two."}, suggest("c2", "B"), {text: "Three."}}}
	store := newMemStore()
	bridge := mustNew(t, librarian(t, llm, ts), WithThreadStore(store))
	run := func(runID string, messages []aguix.Message) []aguix.Message {
		t.Helper()
		if s := serve(t, bridge, input(runID, messages...)); s.last()["type"] != "RUN_FINISHED" {
			t.Fatalf("%s:\n%s", runID, s.body)
		}
		return store.thread("anonymous", "thread-1")
	}
	content := func(ms []aguix.Message) string {
		i := slices.IndexFunc(ms, func(m aguix.Message) bool { return m.Role == "activity" })
		if i < 0 {
			return ""
		}
		return string(ms[i].Content)
	}

	first := run("run-1", []aguix.Message{text("user", "One.")})
	if content(first) != `{"prompts":["A"]}` {
		t.Fatalf("the first turn stored %v", ids(first))
	}
	second := run("run-2", clientSends(first, text("user", "Two.")))
	if !slices.Equal(ids(second[:len(first)]), ids(first)) || content(second) != content(first) || len(second) != len(first)+2 {
		t.Errorf("a turn that published nothing kept %v, want %v and two more", ids(second), ids(first))
	}
	third := run("run-3", clientSends(second, text("user", "Three.")))
	if !slices.Equal(ids(third[:len(second)]), ids(second)) || content(third) != `{"prompts":["B"]}` ||
		slices.ContainsFunc(third[len(second):], func(m aguix.Message) bool { return m.Role == "activity" }) {
		t.Errorf("a snapshot of a stored activity did not replace it in place: %v", ids(third))
	}
}

func TestStoredActivitiesGoBackAfterTheMessageTheyFollowed(t *testing.T) {
	act := func(id string) aguix.Message { return aguix.Message{ID: id, Role: "activity"} }
	u1, a1, u2, a2 := text("user", "1"), text("assistant", "1"), text("user", "2"), text("assistant", "2")
	stored := []aguix.Message{act("lead"), u1, act("x"), act("y"), a1, act("sent"), u2, act("gone"), a2}
	got := withActivities(stored, []aguix.Message{u1, a1, act("sent"), text("user", "edited")})
	want := []string{"activity:lead", "user:user-1", "activity:x", "activity:y", "assistant:assistant-1", "activity:sent", "user:user-edited"}
	if !slices.Equal(ids(got), want) {
		t.Errorf("%v\nwant %v", ids(got), want)
	}
	if client := []aguix.Message{u1}; &withActivities([]aguix.Message{u1, a1}, client)[0] != &client[0] {
		t.Error("a thread with no activities should pass the client's messages through")
	}
}

func TestASnapshotThatDoesNotReplaceLeavesAStoredActivity(t *testing.T) {
	r := &recorder{prior: []aguix.Message{{ID: "next", Role: "activity", Content: json.RawMessage(`1`)}}}
	r.observe(aguix.ActivitySnapshot("next", "s", 2).WithReplace(false))
	if string(r.prior[0].Content) != `1` || len(r.messages()) != 0 {
		t.Errorf("%+v %v", r.prior, r.messages())
	}
	r.observe(aguix.ActivitySnapshot("next", "s", 3))
	if string(r.prior[0].Content) != `3` || len(r.messages()) != 0 {
		t.Errorf("%+v %v", r.prior, r.messages())
	}
}
