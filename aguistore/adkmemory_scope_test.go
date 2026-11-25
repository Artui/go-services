package aguistore

import (
	"testing"

	"google.golang.org/adk/v2/session"
)

func TestEveryDialectsMemoryReplacesOnlyTheAddingUsersSession(t *testing.T) {
	forEachMemoryDialect(t, func(t *testing.T, m *ADKMemory) {
		two := []*session.Event{said("e1", "user", "owl one", 0), said("e2", "user", "owl two", 1)}
		remember(t, m, fakeSession{"app", "alice", "th1", two})
		remember(t, m, fakeSession{"other", "alice", "th1", two})
		remember(t, m, fakeSession{"app", "bob", "th1", []*session.Event{said("e1", "user", "owl", 0)}})
		if got := recall(t, m, "app", "alice", "owl"); len(got) != 2 {
			t.Errorf("bob's add took alice's: %q", got)
		}
		remember(t, m, fakeSession{"app", "alice", "th1", two[:1]})
		if got := recall(t, m, "other", "alice", "owl"); len(got) != 2 {
			t.Errorf("another app's add took alice's: %q", got)
		}
	})
}
