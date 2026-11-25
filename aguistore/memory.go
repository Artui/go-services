package aguistore

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/aguix"
)

// Memory is a Store that keeps threads for the life of the process: for
// development, tests and a single process that may forget on restart.
type Memory struct {
	cfg     config
	mu      sync.Mutex
	threads map[string]map[string]*memoryThread
	runs    map[string]map[string]*memoryRun
}

// memoryRun is a thread's run status, kept apart from its messages as SQL keeps
// it, so a run that starts before the thread's first save still records.
type memoryRun struct {
	runID, status, reason string
	interrupts            []byte
	lastRead              time.Time
}

type memoryThread struct {
	Thread
	messages []byte
}

// NewMemory returns an empty Memory.
func NewMemory(opts ...Option) *Memory {
	return &Memory{
		cfg: configure(opts), threads: map[string]map[string]*memoryThread{}, runs: map[string]map[string]*memoryRun{},
	}
}

// LoadThread returns the thread's messages, decoded afresh on every call so a
// caller's changes never reach the store.
func (s *Memory) LoadThread(_ context.Context, userID, threadID string) ([]aguix.Message, bool, error) {
	s.mu.Lock()
	t, ok := s.threads[userID][threadID]
	s.mu.Unlock()
	if !ok {
		return nil, false, nil
	}
	messages, err := decodeMessages(t.messages)
	return messages, err == nil, err
}

// SaveThread stores an encoded copy of messages.
func (s *Memory) SaveThread(_ context.Context, userID, threadID string, messages []aguix.Message) error {
	encoded, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("aguistore: encoding the thread: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mine := s.threads[userID]
	if mine == nil {
		mine = map[string]*memoryThread{}
		s.threads[userID] = mine
	}
	t := mine[threadID]
	if t == nil {
		t = &memoryThread{Thread: Thread{ID: threadID, Title: titleFrom(messages)}}
		mine[threadID] = t
	}
	t.Preview, t.UpdatedAt, t.messages = previewFrom(messages), s.cfg.now(), encoded
	return nil
}

// ListThreads returns the user's threads, newest first.
func (s *Memory) ListThreads(_ context.Context, userID string, limit int) ([]Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Thread, 0, len(s.threads[userID]))
	for id, t := range s.threads[userID] {
		row := t.Thread
		if run := s.runs[userID][id]; run != nil {
			row.Status, row.Reason, row.LastReadAt = run.status, run.reason, run.lastRead
			row.Interrupts = slices.Clone(run.interrupts)
		}
		out = append(out, row)
	}
	slices.SortFunc(out, newestFirst)
	return out[:min(len(out), max(limit, 0))], nil
}

// RenameThread sets the thread's title.
func (s *Memory) RenameThread(_ context.Context, userID, threadID, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.threads[userID][threadID]
	if !ok {
		return fmt.Errorf("%w: thread %q", services.ErrNotFound, threadID)
	}
	t.Title = title
	return nil
}

// DeleteThread removes the thread.
func (s *Memory) DeleteThread(_ context.Context, userID, threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.threads[userID], threadID)
	delete(s.runs[userID], threadID)
	return nil
}

// StartRun marks the thread running, or claims it for a resume.
func (s *Memory) StartRun(_ context.Context, userID, threadID, runID string, resumes []string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.run(userID, threadID)
	if len(resumes) > 0 && !claimable(run.status, run.interrupts, resumes) {
		return false, nil
	}
	run.runID, run.status, run.reason, run.interrupts = runID, StatusRunning, "", nil
	return true, nil
}

// FinishRun records how runID ended, unless another run has replaced it.
func (s *Memory) FinishRun(
	_ context.Context, userID, threadID, runID, status, reason string, interrupts json.RawMessage,
) error {
	if err := checkFinish(status, interrupts); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[userID][threadID]; run != nil && run.runID == runID {
		run.status, run.reason, run.interrupts = status, reason, slices.Clone(interrupts)
	}
	return nil
}

// MarkRead stamps the thread read.
func (s *Memory) MarkRead(_ context.Context, userID, threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.threads[userID][threadID]; !ok {
		return fmt.Errorf("%w: thread %q", services.ErrNotFound, threadID)
	}
	s.run(userID, threadID).lastRead = s.cfg.now()
	return nil
}

// run returns the thread's run row, creating an idle one; s.mu is held.
func (s *Memory) run(userID, threadID string) *memoryRun {
	mine := s.runs[userID]
	if mine == nil {
		mine = map[string]*memoryRun{}
		s.runs[userID] = mine
	}
	run := mine[threadID]
	if run == nil {
		run = &memoryRun{status: StatusIdle}
		mine[threadID] = run
	}
	return run
}

func newestFirst(a, b Thread) int {
	if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
		return c
	}
	return cmp.Compare(a.ID, b.ID)
}

func decodeMessages(raw []byte) ([]aguix.Message, error) {
	var messages []aguix.Message
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil, fmt.Errorf("aguistore: decoding the thread: %w", err)
	}
	return messages, nil
}
