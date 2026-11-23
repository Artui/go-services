package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Artui/go-services/example"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// scripted answers as a model does: it calls the tool it is told to on its
// first turn and speaks once it has seen the tool's result.
type scripted struct {
	tool string
	args map[string]any
}

func (scripted) Name() string { return "scripted" }

func (s scripted) GenerateContent(
	_ context.Context, req *model.LLMRequest, _ bool,
) iter.Seq2[*model.LLMResponse, error] {
	last := req.Contents[len(req.Contents)-1]
	part := genai.NewPartFromText("Done.")
	if last.Parts[0].FunctionResponse == nil {
		part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: "call-1", Name: s.tool, Args: s.args}}
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{
			Content:      &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{part}},
			TurnComplete: true,
		}, nil)
	}
}

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := example.Open(ctx, strings.ReplaceAll(t.Name(), "/", "_"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := example.Seed(ctx, db); err != nil {
		t.Fatal(err)
	}
	return db
}

func run(t *testing.T, llm model.LLM, db *sql.DB) []map[string]any {
	t.Helper()
	h, err := newHandler(llm, db)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(
		`{"threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":"borrow book 10"}]}`)))
	var frames []map[string]any
	for _, block := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n") {
		var f map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(block, "data: ")), &f); err != nil {
			t.Fatalf("frame is not JSON: %q", block)
		}
		frames = append(frames, f)
	}
	return frames
}

func loans(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM loans`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A tool-using turn, on the wire and in the database: the model's call to a
// spec streams as TOOL_CALL frames, runs inside the kernel's transaction as the
// signed-in member, and the model answers after its result in the same run.
func TestAToolTurnBorrowsABook(t *testing.T) {
	db := newDB(t)
	before := loans(t, db)
	frames := run(t, scripted{tool: "borrow_book", args: map[string]any{"book_id": 10}}, db)

	var types []string
	for _, f := range frames {
		types = append(types, fmt.Sprint(f["type"]))
	}
	want := []string{
		"RUN_STARTED", "TOOL_CALL_START", "TOOL_CALL_ARGS", "TOOL_CALL_END", "TOOL_CALL_RESULT",
		"TEXT_MESSAGE_START", "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END", "RUN_FINISHED",
	}
	if fmt.Sprint(types) != fmt.Sprint(want) {
		t.Errorf("frames = %v\nwant %v", types, want)
	}
	if n := loans(t, db); n != before+1 {
		t.Errorf("loans = %d, want %d", n, before+1)
	}
}

// Identity is the request's member: outside the middleware there is none, and
// the bridge fails the run on the error before the model is asked.
func TestARunWithoutAMemberIsRefused(t *testing.T) {
	if _, err := userID(context.Background()); err == nil {
		t.Error("userID answered for a context with no member")
	}
}

// The page loads the successor component, not the retired one.
func TestThePageLoadsTheSuccessor(t *testing.T) {
	if !strings.Contains(html, "@artooi/agents-web-components@") ||
		!strings.Contains(html, "/dist/agents-web-components.bundle.js") ||
		!strings.Contains(html, "defineChat()") {
		t.Error("the page does not load agents-web-components and define the chat")
	}
	if strings.Contains(html, "ag-ui-web-component") {
		t.Error("the page still names the retired package")
	}
}
