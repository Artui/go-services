package conformance_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Artui/go-services/aguistore"
	"github.com/Artui/go-services/aguix"
)

// parkedThread parks th1 on an approval and returns the resume the web
// component would send, and the interrupts RUN_FINISHED carried.
func parkedThread(t *testing.T, store *aguistore.SQL, retired *atomic.Int32) (aguix.RunInput, string) {
	t.Helper()
	ask := aguix.Message{ID: "u1", Role: "user", Content: aguix.TextContent("Retire author 7.")}
	paused := post(t, approving(t, store, retired, nil), aguix.RunInput{ThreadID: "th1", RunID: "r1", Messages: []aguix.Message{ask}})
	var outcome struct {
		Interrupts []aguix.Interrupt `json:"interrupts"`
	}
	if err := json.Unmarshal(paused["outcome"], &outcome); err != nil || len(outcome.Interrupts) != 1 {
		t.Fatalf("the run did not park: %s %v", paused["outcome"], err)
	}
	_, body := get(t, threadsOf(store), "/threads/th1/")
	var thread struct{ Messages []aguix.Message }
	if err := json.Unmarshal([]byte(body), &thread); err != nil {
		t.Fatal(err)
	}
	return aguix.RunInput{ThreadID: "th1", RunID: "r2", Messages: thread.Messages, Resume: []aguix.ResumeEntry{
		{InterruptID: outcome.Interrupts[0].ID, Status: "resolved", Payload: json.RawMessage(`{"approved":true}`)},
	}}, string(paused["outcome"])
}

// cutAfterClaim drops the connection once a run's claim has won.
type cutAfterClaim struct {
	*aguistore.SQL
	drop context.CancelFunc
}

func (c cutAfterClaim) StartRun(
	ctx context.Context, userID, threadID, runID string, resumes []string,
) (json.RawMessage, bool, error) {
	claimed, won, err := c.SQL.StartRun(ctx, userID, threadID, runID, resumes)
	c.drop()
	return claimed, won, err
}

func threadsOf(store *aguistore.SQL) http.Handler {
	return asUser("alice", http.StripPrefix("/threads", aguistore.Handler(store, caller)))
}

// postWithin sends in under ctx, as a connection the client drops does.
func postWithin(ctx context.Context, h http.Handler, in aguix.RunInput) {
	body, _ := json.Marshal(in)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, "POST", "/", strings.NewReader(string(body))))
}

func listed(t *testing.T, store *aguistore.SQL) (status, interrupts string) {
	t.Helper()
	_, body := get(t, threadsOf(store), "/threads/")
	var list listing
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list.Threads) != 1 {
		t.Fatalf("%s %v", body, err)
	}
	return list.Threads[0].Status, string(list.Threads[0].Interrupts)
}

func TestAResumeCutOffBeforeItsCallRanCanBeSentAgain(t *testing.T) {
	_, store := openStore(t, filepath.Join(t.TempDir(), "threads.db"))
	var retired atomic.Int32
	resume, outcome := parkedThread(t, store, &retired)
	gone, cancel := context.WithCancel(context.Background())
	defer cancel()
	postWithin(gone, approving(t, cutAfterClaim{store, cancel}, &retired, nil), resume)
	if status, interrupts := listed(t, store); status != "waiting" || interrupts == "" || !strings.Contains(outcome, interrupts) || retired.Load() != 0 {
		t.Errorf("after a dropped resume: %s %s, the call ran %d times; RUN_FINISHED carried %s", status, interrupts, retired.Load(), outcome)
	}
	resume.RunID = "r3"
	if got := post(t, approving(t, store, &retired, nil), resume); string(got["type"]) != `"RUN_FINISHED"` || retired.Load() != 1 {
		t.Errorf("the retry: %v, the call ran %d times", got, retired.Load())
	}
}

func TestAResumeCutOffAfterItsCallRanIsNotRunAgain(t *testing.T) {
	_, store := openStore(t, filepath.Join(t.TempDir(), "threads.db"))
	var retired atomic.Int32
	resume, _ := parkedThread(t, store, &retired)
	hold := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		postWithin(ctx, approving(t, store, &retired, hold), resume)
	}()
	<-hold
	cancel()
	hold <- struct{}{}
	<-done
	if status, _ := listed(t, store); status == "waiting" {
		t.Errorf("a resume whose call ran parked again")
	}
	resume.RunID = "r3"
	if got := post(t, approving(t, store, &retired, nil), resume); string(got["type"]) != `"RUN_ERROR"` || retired.Load() != 1 {
		t.Errorf("the retry: %v, the call ran %d times", got, retired.Load())
	}
}
