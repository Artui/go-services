package aguiadk

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
)

// dropping cancels each request once its run's claim has been won.
func dropping(store *runMem) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			store.won = cancel
			next.ServeHTTP(w, r.WithContext(ctx))
			store.won = nil
		})
	}
}

func TestAResumeDroppedBeforeItsCallParksItsClaimAgain(t *testing.T) {
	a := newArchive(t)
	store := newRunMem()
	bridge, _ := parkedBridge(t, a, store, step{text: "Discarded."})
	parked := store.row("thread-1").interrupts
	serve(t, bridge, approve("run-a"), dropping(store))
	if row := store.row("thread-1"); row.status != "waiting" || row.run != "run-a" || string(row.interrupts) != string(parked) || len(a.ran()) != 0 {
		t.Fatalf("after a dropped resume: %+v %s, ran %v", row, row.interrupts, a.ran())
	}
	if s := serve(t, bridge, approve("run-b")); s.last()["type"] != "RUN_FINISHED" || !slices.Equal(a.ran(), []string{"discard 7"}) {
		t.Errorf("the retry ran %v:\n%s", a.ran(), s.body)
	}
}

func TestAResumeDroppedAfterItsCallRanIsRefusedAgain(t *testing.T) {
	a, entered, release := slowArchive(t)
	store := newRunMem()
	bridge, _ := parkedBridge(t, a, store, step{text: "Discarded."}, step{text: "Again."})
	ctx, cancel := context.WithCancel(context.Background())
	cut := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(w, r.WithContext(ctx)) })
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		serve(t, bridge, approve("run-a"), cut)
	}()
	<-entered
	cancel()
	close(release)
	<-done
	if row := store.row("thread-1"); row.status == "waiting" {
		t.Errorf("a resume whose call ran parked again: %+v", row)
	}
	s := serve(t, bridge, approve("run-b"))
	if len(s.errs) != 1 || !errors.Is(s.errs[0], errResumeTaken) || errResumeTaken.Error() != "aguiadk: the interrupts this run answers are no longer waiting" {
		t.Errorf("%v", s.errs)
	}
	if ran := a.ran(); !slices.Equal(ran, []string{"discard 7"}) {
		t.Errorf("ran %v", ran)
	}
}

func TestADeniedResumeThatFinishesLeavesTheThreadIdle(t *testing.T) {
	a := newArchive(t)
	store := newRunMem()
	bridge, _ := parkedBridge(t, a, store, step{text: "Left it."})
	deny := approve("run-a")
	deny.Resume[0].Payload = []byte(`{"approved":false}`)
	if s := serve(t, bridge, deny); s.last()["type"] != "RUN_FINISHED" {
		t.Fatalf("%s", s.body)
	}
	if row := store.row("thread-1"); row.status != "idle" || len(a.ran()) != 0 {
		t.Errorf("%+v, ran %v", row, a.ran())
	}
}

func TestARunThatSucceedsNeverParksItsClaimAgain(t *testing.T) {
	store := newRunMem()
	b := &bridge{runs: store}
	if err := b.endRun(context.Background(), "u", approve("run-a"), nil, nil, []byte(`[{"id":"int-c1"}]`)); err != nil {
		t.Fatal(err)
	}
	if got := store.ends; len(got) != 1 || got[0].status != "idle" || got[0].interrupts != nil {
		t.Errorf("ends %+v", got)
	}
}
