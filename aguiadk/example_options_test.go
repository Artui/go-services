package aguiadk_test

import (
	"context"
	"fmt"
	"iter"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"

	"github.com/Artui/go-services/aguiadk"
	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// replyLLM answers every call with one fixed reply and keeps the last request.
type replyLLM struct {
	reply string
	last  *model.LLMRequest
}

func (m *replyLLM) Name() string { return "reply" }

func (m *replyLLM) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.last = req
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: genai.NewContentFromText(m.reply, genai.RoleModel), TurnComplete: true}, nil)
	}
}

func (m *replyLLM) instruction() string {
	var text []string
	if si := m.last.Config.SystemInstruction; si != nil {
		for _, p := range si.Parts {
			text = append(text, p.Text)
		}
	}
	return strings.Join(text, "\n")
}

func (m *replyLLM) declared() []string {
	var names []string
	for _, t := range m.last.Config.Tools {
		for _, fd := range t.FunctionDeclarations {
			names = append(names, fd.Name)
		}
	}
	slices.Sort(names)
	return names
}

// run posts one AG-UI run to a bridge over root, as the web component would.
func run(llm *replyLLM, toolsets []tool.Toolset, body string, opts ...aguiadk.Option) {
	root, err := llmagent.New(llmagent.Config{Name: "assistant", Model: llm, Toolsets: toolsets})
	if err != nil {
		log.Fatal(err)
	}
	bridge, err := aguiadk.New(root, append([]aguiadk.Option{aguiadk.WithUserID(aguiadk.Anonymous)}, opts...)...)
	if err != nil {
		log.Fatal(err)
	}
	handler, err := aguix.Handler(bridge)
	if err != nil {
		log.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		log.Fatal(rec.Code, rec.Body.String())
	}
}

// threads is a ThreadStore in a map, as a test or a single process might keep one.
type threads struct {
	mu   sync.Mutex
	byID map[string][]aguix.Message
}

func (s *threads) LoadThread(_ context.Context, userID, threadID string) ([]aguix.Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byID[userID+"/"+threadID]
	return m, ok, nil
}

func (s *threads) SaveThread(_ context.Context, userID, threadID string, messages []aguix.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[userID+"/"+threadID] = messages
	return nil
}

// The thread is saved when the run ends: the client's messages, then the answer.
func ExampleWithThreadStore() {
	store := &threads{byID: map[string][]aguix.Message{}}
	run(&replyLLM{reply: "Hello, Ada."}, nil, `{"threadId": "th1", "runId": "r1",
		"messages": [{"id": "u1", "role": "user", "content": "Hi, I am Ada."}]}`,
		aguiadk.WithThreadStore(store))

	saved, _, _ := store.LoadThread(context.Background(), "anonymous", "th1")
	for _, m := range saved {
		fmt.Printf("%s: %s\n", m.Role, m.Text())
	}
	// Output:
	// user: Hi, I am Ada.
	// assistant: Hello, Ada.
}

// The palette lists the skills without their bodies; a run that opens with
// /name adds that skill's body to the model's instructions.
func ExampleWithSkills() {
	skills := map[string]aguiadk.Skill{
		"triage": {Title: "Triage", Body: "Sort the open tickets by urgency.", Chip: true},
	}
	catalog := httptest.NewRecorder()
	aguiadk.SkillCatalog(skills).ServeHTTP(catalog, httptest.NewRequest(http.MethodGet, "/agent/skills", nil))
	fmt.Println(catalog.Body.String())

	llm := &replyLLM{reply: "Sorted."}
	run(llm, nil, `{"threadId": "th1", "runId": "r1",
		"messages": [{"id": "u1", "role": "user", "content": "/triage"}]}`,
		aguiadk.WithSkills(skills))
	fmt.Println(strings.Contains(llm.instruction(), "# Skill: triage\n\nSort the open tickets by urgency."))
	// Output:
	// [{"name":"triage","title":"Triage","chip":true}]
	// true
}

// The client's context reaches the model as a fenced block of data.
func ExampleWithRunContext() {
	llm := &replyLLM{reply: "You are on the invoices page."}
	run(llm, nil, `{"threadId": "th1", "runId": "r1",
		"messages": [{"id": "u1", "role": "user", "content": "Where am I?"}],
		"context": [{"description": "Current page", "value": "/invoices"}]}`,
		aguiadk.WithRunContext())
	text := llm.instruction()
	start := strings.Index(text, "description: ")
	fmt.Println(text[start : start+strings.Index(text[start:], "/invoices")+len("/invoices")])
	// Output:
	// description: Current page
	// value:
	// /invoices
}

// Of the browser tools the run lists, the model is offered only the allowed ones.
func ExampleAllowClientTools() {
	llm := &replyLLM{reply: "Done."}
	run(llm, []tool.Toolset{aguiadk.ClientToolset(aguiadk.AllowClientTools("open_invoice"))},
		`{"threadId": "th1", "runId": "r1",
		"messages": [{"id": "u1", "role": "user", "content": "Open the latest invoice."}],
		"tools": [
			{"name": "open_invoice", "description": "Open an invoice.", "parameters": {"type": "object"}},
			{"name": "delete_account", "description": "Delete the account.", "parameters": {"type": "object"}}
		]}`)
	fmt.Println(llm.declared())
	// Output:
	// [open_invoice]
}
