package conformance_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Artui/go-services/aguiadk"
	"github.com/Artui/go-services/aguistore"
	"github.com/Artui/go-services/aguix"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
	_ "modernc.org/sqlite"
)

var (
	_ aguiadk.ThreadStore = (*aguistore.Memory)(nil)
	_ aguiadk.ThreadStore = (*aguistore.SQL)(nil)
)

// greeter answers every request with one complete text response.
type greeter struct{}

func (greeter) Name() string { return "greeter" }

func (greeter) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Hello.", genai.RoleModel), TurnComplete: true}, nil)
	}
}

type userKey struct{}

// caller is the one function both the bridge and the threads handler resolve
// the user with, as the store's documentation asks.
func caller(ctx context.Context) (string, error) {
	user, _ := ctx.Value(userKey{}).(string)
	return user, nil
}

func asUser(user string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}

func openStore(t *testing.T, path string) (*sql.DB, *aguistore.SQL) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := aguistore.OpenSQL(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return db, store
}

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	body, _ := io.ReadAll(w.Result().Body)
	return w.Code, string(body)
}

func TestAThreadARunSavedIsServedAfterARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "threads.db")
	db, store := openStore(t, path)
	root, err := llmagent.New(llmagent.Config{Name: "greeter", Model: greeter{}})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := aguiadk.New(root, aguiadk.WithUserID(caller), aguiadk.WithThreadStore(store))
	if err != nil {
		t.Fatal(err)
	}
	runs, err := aguix.Handler(bridge)
	if err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(aguix.RunInput{ThreadID: "th1", RunID: "r1", Messages: []aguix.Message{
		{ID: "u1", Role: "user", Content: aguix.TextContent("Hi, I am Alice.")},
	}})
	w := httptest.NewRecorder()
	asUser("alice", runs).ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(string(in))))
	if !strings.Contains(w.Body.String(), "RUN_FINISHED") {
		t.Fatalf("the run did not finish:\n%s", w.Body)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, reopened := openStore(t, path)
	threads := http.StripPrefix("/threads", aguistore.Handler(reopened, caller))
	if code, body := get(t, asUser("alice", threads), "/threads/"); code != 200 ||
		!strings.Contains(body, `"thread_id":"th1","title":"Hi, I am Alice."`) || !strings.Contains(body, `"preview":"Hello."`) {
		t.Errorf("the list: %d %s", code, body)
	}
	code, body := get(t, asUser("alice", threads), "/threads/th1/")
	var thread struct{ Messages []aguix.Message }
	if err := json.Unmarshal([]byte(body), &thread); code != 200 || err != nil || len(thread.Messages) != 2 ||
		thread.Messages[0].Text() != "Hi, I am Alice." || thread.Messages[1].Role != "assistant" ||
		thread.Messages[1].Text() != "Hello." {
		t.Errorf("the thread: %d %s", code, body)
	}
	if code, _ := get(t, asUser("bob", threads), "/threads/th1/"); code != http.StatusNotFound {
		t.Errorf("bob read alice's thread: %d", code)
	}
}
