package aguistore

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
)

// The statuses a thread's runs leave it in, as the web component's history
// panel reads them.
const (
	StatusIdle    = "idle"
	StatusRunning = "running"
	StatusWaiting = "waiting"
	StatusFailed  = "failed"
)

// RunStore is a Store that also records each thread's run status: whether a
// run is going, failed, or parked on interrupts the next run must answer. Both
// Memory and SQL are one. It satisfies aguiadk's RunStore, so the bridge
// records every run through it, and Handler then lists each thread with its
// status, reason, interrupts and last_read_at, and serves POST /<id>/read/.
type RunStore interface {
	Store
	// StartRun marks the thread running under runID. With no resumes it always
	// starts. With resumes, the interrupt ids a run answers, it is a claim: it
	// starts only while the thread waits on every one of them, so the same
	// answer sent twice runs once, and it returns false when the claim is lost.
	// A claim that wins returns the interrupts it took, so a run cut off before
	// it ran a call can park them again.
	StartRun(ctx context.Context, userID, threadID, runID string, resumes []string) (claimed json.RawMessage, won bool, err error)
	// FinishRun records how runID ended: its status, the reason it failed,
	// and the interrupts it parked on, kept as the bytes RUN_FINISHED carried.
	// A run another has since replaced on the thread changes nothing.
	FinishRun(ctx context.Context, userID, threadID, runID, status, reason string, interrupts json.RawMessage) error
	// MarkRead stamps the thread read now. A thread the user does not have is
	// an error wrapping services.ErrNotFound.
	MarkRead(ctx context.Context, userID, threadID string) error
}

// claimable reports whether a thread parked on stored may be resumed by an
// answer to every interrupt in resumes.
func claimable(status string, stored []byte, resumes []string) bool {
	if status != StatusWaiting || len(resumes) == 0 {
		return false
	}
	var parked []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(stored, &parked); err != nil {
		return false
	}
	ids := make([]string, 0, len(parked))
	for _, p := range parked {
		ids = append(ids, p.ID)
	}
	for _, id := range resumes {
		if !slices.Contains(ids, id) {
			return false
		}
	}
	return true
}

// checkFinish refuses a status no run ends in, and interrupts off a waiting one.
func checkFinish(status string, interrupts json.RawMessage) error {
	switch status {
	case StatusIdle, StatusFailed:
		if len(interrupts) > 0 {
			return fmt.Errorf("aguistore: a %s run parks on no interrupts", status)
		}
	case StatusWaiting:
		if !json.Valid(interrupts) || interrupts[0] != '[' {
			return fmt.Errorf("aguistore: a waiting run needs its interrupts as a JSON array")
		}
	default:
		return fmt.Errorf("aguistore: a run cannot end %q", status)
	}
	return nil
}
