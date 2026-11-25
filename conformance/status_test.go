package conformance_test

import (
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"github.com/Artui/go-services/aguiadk"
	"github.com/Artui/go-services/aguistore"
	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

var (
	_ aguiadk.RunStore = (*aguistore.Memory)(nil)
	_ aguiadk.RunStore = (*aguistore.SQL)(nil)
)

// retirer calls retire_author once, then answers the call's result.
type retirer struct{}

func (retirer) Name() string { return "retirer" }

func (retirer) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	answered := slices.ContainsFunc(req.Contents, func(c *genai.Content) bool {
		return slices.ContainsFunc(c.Parts, func(p *genai.Part) bool {
			return p.FunctionResponse != nil && p.FunctionResponse.Name == "retire_author"
		})
	})
	reply := genai.NewContentFromText("Retired.", genai.RoleModel)
	if !answered {
		reply = &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "c1", Name: "retire_author", Args: map[string]any{"id": 7},
		}}}}
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: reply, TurnComplete: true}, nil)
	}
}

type retireIn struct {
	ID int64 `json:"id"`
}

// approving is a bridge over store whose one destructive spec waits for
// approval; retired counts its calls, the first held while hold is open.
func approving(t *testing.T, store aguiadk.ThreadStore, retired *atomic.Int32, hold chan struct{}) http.Handler {
	t.Helper()
	yes := true
	reg := services.New[struct{}](nil)
	services.MustRegister(reg, services.Spec[struct{}, retireIn, retireIn]{
		Name: "retire_author", Description: "Retire an author for good.", Kind: services.Mutation, Destructive: &yes,
		Run: func(c services.Ctx[struct{}], in retireIn) (retireIn, error) {
			if retired.Add(1) == 1 && hold != nil {
				hold <- struct{}{}
				<-hold
			}
			_, err := aguiadk.PublishSuggestions(c.Context, "next", "Retire another author")
			return in, err
		},
	})
	ts, err := adkx.Toolset(reg, adkx.Anonymous)
	if err != nil {
		t.Fatal(err)
	}
	approval := aguiadk.NewApproval(reg.Entries())
	root, err := llmagent.New(llmagent.Config{Name: "retirer", Model: retirer{}, Toolsets: []tool.Toolset{approval.Gate(ts)}})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := aguiadk.New(root, aguiadk.WithUserID(caller), aguiadk.WithThreadStore(store), aguiadk.WithApproval(approval))
	if err != nil {
		t.Fatal(err)
	}
	runs, err := aguix.Handler(bridge)
	if err != nil {
		t.Fatal(err)
	}
	return asUser("alice", runs)
}

// post runs in and returns its last frame.
func post(t *testing.T, h http.Handler, in aguix.RunInput) map[string]json.RawMessage {
	t.Helper()
	body, _ := json.Marshal(in)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(string(body))))
	frames := strings.Split(strings.TrimSpace(w.Body.String()), "\n\n")
	var last map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimPrefix(frames[len(frames)-1], "data: ")), &last); err != nil {
		t.Fatalf("%v:\n%s", err, w.Body)
	}
	return last
}

type listing struct {
	Threads []struct {
		ID         string          `json:"thread_id"`
		Status     string          `json:"status"`
		Interrupts json.RawMessage `json:"interrupts"`
	} `json:"threads"`
}

func TestAParkedApprovalSurvivesARestartAndItsResumeRunsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "threads.db")
	var retired atomic.Int32
	db, store := openStore(t, path)
	ask := aguix.Message{ID: "u1", Role: "user", Content: aguix.TextContent("Retire author 7.")}
	paused := post(t, approving(t, store, &retired, nil), aguix.RunInput{ThreadID: "th1", RunID: "r1", Messages: []aguix.Message{ask}})
	var outcome struct {
		Type       string          `json:"type"`
		Interrupts json.RawMessage `json:"interrupts"`
	}
	if err := json.Unmarshal(paused["outcome"], &outcome); err != nil || outcome.Type != "interrupt" {
		t.Fatalf("the run did not park: %s %v", paused["outcome"], err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, store = openStore(t, path)
	threads := asUser("alice", http.StripPrefix("/threads", aguistore.Handler(store, caller)))
	_, body := get(t, threads, "/threads/")
	var list listing
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list.Threads) != 1 || list.Threads[0].Status != "waiting" ||
		string(list.Threads[0].Interrupts) != string(outcome.Interrupts) {
		t.Fatalf("the list after a restart: %s\nRUN_FINISHED carried %s", body, outcome.Interrupts)
	}
	var restored []aguix.Interrupt
	if err := json.Unmarshal(list.Threads[0].Interrupts, &restored); err != nil || len(restored) != 1 || restored[0].ToolCallID != "c1" {
		t.Fatalf("the restored interrupts: %v %v", restored, err)
	}
	_, body = get(t, threads, "/threads/th1/")
	var thread struct{ Messages []aguix.Message }
	if err := json.Unmarshal([]byte(body), &thread); err != nil || len(thread.Messages) != 2 ||
		len(thread.Messages[1].ToolCalls) != 1 || thread.Messages[1].ToolCalls[0].ID != "c1" {
		t.Fatalf("the parked thread should end on its unanswered call: %s", body)
	}

	// The web component resumes from the thread it read and the interrupt the list restored.
	resume := aguix.RunInput{ThreadID: "th1", RunID: "r2", Messages: thread.Messages, Resume: []aguix.ResumeEntry{
		{InterruptID: restored[0].ID, Status: "resolved", Payload: json.RawMessage(`{"approved":true}`)},
	}}
	// A second resume sent while the first is inside the call is the one the
	// stored history cannot refuse: only the claim does.
	hold := make(chan struct{})
	runs := approving(t, store, &retired, hold)
	first := make(chan map[string]json.RawMessage)
	go func() { first <- post(t, runs, resume) }()
	<-hold
	again := resume
	again.RunID = "r3"
	second := post(t, runs, again)
	hold <- struct{}{}
	if got := <-first; string(got["type"]) != `"RUN_FINISHED"` {
		t.Fatalf("the resume: %v", got)
	}
	if string(second["type"]) != `"RUN_ERROR"` || retired.Load() != 1 {
		t.Errorf("a second resume: %v, the call ran %d times", second, retired.Load())
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, store = openStore(t, path)
	threads = asUser("alice", http.StripPrefix("/threads", aguistore.Handler(store, caller)))
	if _, body := get(t, threads, "/threads/"); !strings.Contains(body, `"status":"idle"`) {
		t.Errorf("the list after the resume: %s", body)
	}
	_, body = get(t, threads, "/threads/th1/")
	thread.Messages = nil
	if err := json.Unmarshal([]byte(body), &thread); err != nil {
		t.Fatal(err)
	}
	roles := make([]string, 0, len(thread.Messages))
	for _, m := range thread.Messages {
		roles = append(roles, m.Role)
	}
	at := slices.Index(roles, "activity")
	if !slices.Equal(roles, []string{"user", "assistant", "tool", "activity", "assistant"}) ||
		thread.Messages[at].ActivityType != aguiadk.SuggestionsActivityType ||
		string(thread.Messages[at].Content) != `{"prompts":["Retire another author"]}` {
		t.Errorf("the thread after a second restart: %s", body)
	}
}
