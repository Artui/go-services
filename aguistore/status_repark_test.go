package aguistore

import (
	"context"
	"testing"
)

func TestAClaimReturnsTheInterruptsItTookSoTheyCanParkAgain(t *testing.T) {
	eachRuns(t, func(t *testing.T, s RunStore) {
		ctx := context.Background()
		mustSave(t, s, "alice", "th1", msg("u1", "user", "Delete the book."))
		if claimed, ok, err := s.StartRun(ctx, "alice", "th1", "r1", nil); claimed != nil || !ok || err != nil {
			t.Errorf("a start took %s %v %v", claimed, ok, err)
		}
		finish(t, s, "r1", StatusWaiting, "", parked)
		if claimed, ok, err := s.StartRun(ctx, "alice", "th1", "r2", []string{"int-other"}); claimed != nil || ok || err != nil {
			t.Errorf("a lost claim took %s %v %v", claimed, ok, err)
		}
		claimed, ok, err := s.StartRun(ctx, "alice", "th1", "r2", []string{"int-c1"})
		if string(claimed) != parked || !ok || err != nil {
			t.Fatalf("the claim took %s %v %v", claimed, ok, err)
		}
		finish(t, s, "r2", StatusWaiting, "", string(claimed))
		if got := row(t, s, "alice", "th1"); got.Status != StatusWaiting || string(got.Interrupts) != parked {
			t.Errorf("parked again: %+v", got)
		}
		if !start(t, s, "r3", "int-c1") {
			t.Error("a park put back could not be claimed")
		}
	})
}
