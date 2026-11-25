package aguistore

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/aguix"
)

// Memory is a Store that keeps threads for the life of the process: for
// development, tests and a single process that may forget on restart.
type Memory struct {
	cfg     config
	mu      sync.Mutex
	threads map[string]map[string]*memoryThread
}

type memoryThread struct {
	Thread
	messages []byte
}

// NewMemory returns an empty Memory.
func NewMemory(opts ...Option) *Memory {
	return &Memory{cfg: configure(opts), threads: map[string]map[string]*memoryThread{}}
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
	for _, t := range s.threads[userID] {
		out = append(out, t.Thread)
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
	return nil
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
