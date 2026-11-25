package aguiadk

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
)

// WithMemory gives the agent ADK's long-term memory, svc: ADK's load_memory
// and preload_memory tools search it, scoped to the run's user (WithUserID),
// so a user recalls their own threads and never another's. Each run that
// finishes is added to it once, as the thread's whole conversation so far:
// not a run that fails, is cancelled, or stops on an interrupt or a pending
// browser call, whose resumed run is the one added. A service keyed by
// session, as ADK's in-memory one and aguistore's ADKMemory are, keeps one
// copy per thread, the latest. New refuses a nil service.
func WithMemory(svc memory.Service) Option {
	return func(b *bridge) {
		if svc == nil {
			b.optErr = errors.New("aguiadk: WithMemory needs a memory service")
			return
		}
		b.memory = svc
	}
}

// remember adds the run's finished session to the memory service, if any.
func (b *bridge) remember(ctx context.Context, svc session.Service, userID, threadID string) error {
	if b.memory == nil {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	got, err := svc.Get(ctx, &session.GetRequest{AppName: appName, UserID: userID, SessionID: threadID})
	if err != nil {
		return fmt.Errorf("aguiadk: reading the run's session for memory: %w", err)
	}
	if err := b.memory.AddSessionToMemory(ctx, got.Session); err != nil {
		return fmt.Errorf("aguiadk: adding the run to memory: %w", err)
	}
	return nil
}
