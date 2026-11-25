package aguiadk

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

const triageBody = "TRIAGE-BODY: sort the inbox by urgency."

var skills = map[string]Skill{
	"triage":    {Title: "Triage", Description: "Sort the inbox.", Body: triageBody, SendImmediately: true, Chip: true},
	"summarize": {Body: "Summarise the page."},
}

// lastUserText is the text of the last user turn the model was sent.
func lastUserText(req *model.LLMRequest) string {
	for i := len(req.Contents) - 1; i >= 0; i-- {
		if c := req.Contents[i]; c.Role == genai.RoleUser && len(c.Parts) > 0 && c.Parts[0].Text != "" {
			return c.Parts[0].Text
		}
	}
	return ""
}

func skillRun(t *testing.T, messages ...aguix.Message) *model.LLMRequest {
	t.Helper()
	llm := &scriptLLM{}
	in := input("run-1", messages...)
	in.Tools = []aguix.ToolDefinition{fillField}
	s := serve(t, mustNew(t, librarian(t, llm, ClientToolset()), WithSkills(skills)), in)
	if len(s.errs) > 0 || llm.calls() != 1 {
		t.Fatalf("errors %v after %d calls", s.errs, llm.calls())
	}
	if strings.Contains(s.body, "TRIAGE-BODY") {
		t.Errorf("the skill's body was streamed")
	}
	return llm.request(0)
}

func TestAPickedSkillReachesTheModelAsInstructions(t *testing.T) {
	for _, sent := range []string{"/triage", "  /triage the inbox"} {
		req := skillRun(t, text("user", sent))
		if instr := instruction(req); !strings.HasSuffix(instr, "\n\n# Skill: triage\n\n"+triageBody) {
			t.Errorf("%q: the body is not in the instructions:\n%s", sent, instr)
		}
		if got := lastUserText(req); got != sent {
			t.Errorf("%q: the user turn became %q", sent, got)
		}
	}
}

func TestASkillHoldsWhileTheRunAnswersABrowserCall(t *testing.T) {
	req := skillRun(t, text("user", "/triage"),
		aguix.Message{ID: "a1", Role: "assistant", ToolCalls: []aguix.ToolCall{{ID: "c1", Type: "function",
			Function: aguix.FunctionCall{Name: "fill_field", Arguments: `{"field":"email"}`}}}},
		aguix.Message{ID: "t1", Role: "tool", ToolCallID: "c1", Content: aguix.TextContent(`{"filled":true}`)},
	)
	if !strings.Contains(instruction(req), "# Skill: triage\n\n"+triageBody) {
		t.Errorf("the continuation lost the skill:\n%s", instruction(req))
	}
}

func TestOnlyThePickedSkillOfTheLatestUserTurnApplies(t *testing.T) {
	cases := map[string][]aguix.Message{
		"unknown name":        {text("user", "/nope")},
		"bare slash":          {text("user", "/")},
		"no slash":            {text("user", "xtriage")},
		"slash not first":     {text("user", "please /triage")},
		"an earlier pick":     {text("user", "/triage"), {ID: "a1", Role: "assistant", Content: aguix.TextContent("Done.")}, text("user", "Thanks.")},
		"a name-prefixed one": {text("user", "/triaged")},
	}
	for name, messages := range cases {
		req := skillRun(t, messages...)
		if instr := instruction(req); strings.Contains(instr, "# Skill:") {
			t.Errorf("%s: a skill was added:\n%s", name, instr)
		}
		if got, want := lastUserText(req), messages[len(messages)-1].Text(); got != want {
			t.Errorf("%s: the user turn became %q", name, got)
		}
	}
}

func TestASkillComesBeforeTheContextBlock(t *testing.T) {
	llm := &scriptLLM{}
	in := input("run-1", text("user", "/triage"))
	in.Context = []aguix.ContextItem{{Description: "page_map", Value: pageMap}}
	serve(t, mustNew(t, librarian(t, llm), WithSkills(skills), WithRunContext()), in)
	instr := instruction(llm.request(0))
	if !strings.HasSuffix(instr, "# Skill: triage\n\n"+triageBody+"\n\n"+contextBlock(pageMap)) {
		t.Errorf("got:\n%s", instr)
	}
}

func TestWithSkillsRefusesANameNobodyCanPick(t *testing.T) {
	for _, name := range []string{"", "/triage", "two words", "tab\tname"} {
		_, err := New(librarian(t, &scriptLLM{}), WithUserID(Anonymous), WithSkills(map[string]Skill{name: {Body: "x"}}))
		if err == nil || !strings.Contains(err.Error(), "cannot be picked") {
			t.Errorf("%q: %v", name, err)
		}
	}
}

func TestSkillCatalogServesWhatThePaletteParses(t *testing.T) {
	srv := httptest.NewServer(SkillCatalog(skills))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	want := `[{"name":"summarize","title":"summarize"},` +
		`{"name":"triage","title":"Triage","description":"Sort the inbox.","sendImmediately":true,"chip":true}]`
	if string(body) != want {
		t.Errorf("got  %s\nwant %s", body, want)
	}
	// What parseSkills keeps: a string name and title, and no prompt, so a pick sends /name.
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		_, named := row["name"].(string)
		_, titled := row["title"].(string)
		if _, prompted := row["prompt"]; !named || !titled || prompted {
			t.Errorf("the palette would not list %v as a server-resolved skill", row)
		}
	}
}

func TestSkillCatalogAnswersOnlyGet(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodHead, http.MethodPut} {
		rec := httptest.NewRecorder()
		SkillCatalog(skills).ServeHTTP(rec, httptest.NewRequest(method, "/skills", nil))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
			t.Errorf("%s: %d, Allow %q", method, rec.Code, rec.Header().Get("Allow"))
		}
	}
}

func TestSkillCatalogOfNoSkillsIsAnEmptyList(t *testing.T) {
	rec := httptest.NewRecorder()
	SkillCatalog(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/skills", nil))
	if rec.Body.String() != "[]" {
		t.Errorf("got %s", rec.Body)
	}
}

func TestARunWithNoUserMessagePicksNoSkill(t *testing.T) {
	ri := runInstructions{skills: skills}
	if got := ri.skill(input("run-1", aguix.Message{Role: "assistant", Content: aguix.TextContent("/triage")})); got != "" {
		t.Errorf("got %q", got)
	}
}
