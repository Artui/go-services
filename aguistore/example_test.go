package aguistore_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/Artui/go-services/aguistore"
	"github.com/Artui/go-services/aguix"
)

func Example() {
	store := aguistore.NewMemory()
	userID := func(context.Context) (string, error) { return "alice", nil }
	ctx := context.Background()
	_ = store.SaveThread(ctx, "alice", "th1", []aguix.Message{
		{ID: "u1", Role: "user", Content: aguix.TextContent("Where is my order?")},
		{ID: "a1", Role: "assistant", Content: aguix.TextContent("It ships today.")},
	})

	mux := http.NewServeMux()
	mux.Handle("/threads/", http.StripPrefix("/threads", aguistore.Handler(store, userID)))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/threads/th1/", nil))
	fmt.Println(w.Code, w.Body)
	// Output:
	// 200 {"messages":[{"id":"u1","role":"user","content":"Where is my order?"},{"id":"a1","role":"assistant","content":"It ships today."}],"thread_id":"th1"}
}
