package main

import (
	"context"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// answers calls echo on its first turn and speaks once it sees the result.
type answers struct{}

func (answers) Name() string { return "stub" }

func (answers) GenerateContent(
	_ context.Context, req *model.LLMRequest, _ bool,
) iter.Seq2[*model.LLMResponse, error] {
	last := req.Contents[len(req.Contents)-1]
	part := genai.NewPartFromText("The echo said hello.")
	if last.Parts[0].FunctionResponse == nil {
		part = &genai.Part{FunctionCall: &genai.FunctionCall{
			ID: "call-1", Name: "echo", Args: map[string]any{"text": "hello"}}}
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{
			Content:      &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{part}},
			TurnComplete: true,
		}, nil)
	}
}

func post(t *testing.T, h http.Handler, body string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(body)))
	return rec.Body.String()
}

// A spec runs as a tool through tagged modules only: the model's call reaches
// the browser as TOOL_CALL frames and the result comes back in the same run.
func TestToolTurnThroughPublishedTags(t *testing.T) {
	h, err := handler(answers{})
	if err != nil {
		t.Fatal(err)
	}
	out := post(t, h,
		`{"threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":"echo hello"}]}`)
	for _, want := range []string{
		`"type":"TOOL_CALL_START"`, `"toolCallName":"echo"`, `"type":"TOOL_CALL_RESULT"`,
		`The echo said hello.`, `said\":\"hello`, `"type":"RUN_FINISHED"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the run lacks %s:\n%s", want, out)
		}
	}
}
