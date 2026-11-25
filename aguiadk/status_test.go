package aguiadk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	services "github.com/Artui/go-services"
	"github.com/Artui/go-services/adkx"
	"github.com/Artui/go-services/aguix"
	"google.golang.org/genai"
)

// runMem is a RunStore over memStore that claims a resume as aguistore does.
type runMem struct {
	*memStore
	runMu    sync.Mutex
	status   map[string]runRow
	ends     []runRow
	startErr error
	endErr   error
}

type runRow struct {
	run, status, reason string
	interrupts          []byte
}

func newRunMem() *runMem { return &runMem{memStore: newMemStore(), status: map[string]runRow{}} }

func (s *runMem) StartRun(_ context.Context, _, thread, run string, resumes []string) (bool, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.startErr != nil {
		return false, s.startErr
	}
	if len(resumes) > 0 {
		row := s.status[thread]
		var parked []aguix.Interrupt
		_ = json.Unmarshal(row.interrupts, &parked)
		if row.status != "waiting" || !slices.ContainsFunc(parked, func(i aguix.Interrupt) bool { return i.ID == resumes[0] }) {
			return false, nil
		}
	}
	s.status[thread] = runRow{run: run, status: "running"}
	return true, nil
}

func (s *runMem) FinishRun(_ context.Context, _, thread, run, status, reason string, interrupts json.RawMessage) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	row := runRow{run: run, status: status, reason: reason, interrupts: slices.Clone(interrupts)}
	s.ends = append(s.ends, row)
	if s.endErr != nil {
		return s.endErr
	}
	if s.status[thread].run == run {
		s.status[thread] = row
	}
	return nil
}

func (s *runMem) row(thread string) runRow {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.status[thread]
}

// slowArchive is an archive whose destructive call holds its first run until
// release closes, saying when it has started on entered.
func slowArchive(t *testing.T) (a *archive, entered, release chan struct{}) {
	t.Helper()
	a, entered, release = &archive{}, make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	yes := true
	reg := services.New[struct{}](nil)
	services.MustRegister(reg, services.Spec[struct{}, discardIn, discardOut]{
		Name: "discard_book", Description: "Discard a book for good.", Kind: services.Mutation, Destructive: &yes,
		Run: func(_ services.Ctx[struct{}], in discardIn) (discardOut, error) {
			a.record(fmt.Sprintf("discard %d", in.BookID))
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
			return discardOut{Discarded: in.BookID}, nil
		},
	})
	ts, err := adkx.Toolset(reg, adkx.Anonymous)
	if err != nil {
		t.Fatal(err)
	}
	a.entries, a.ts = reg.Entries(), ts
	return a, entered, release
}

func parkedBridge(t *testing.T, a *archive, store ThreadStore, steps ...step) (aguix.Agent, served) {
	t.Helper()
	llm := &scriptLLM{steps: append([]step{{calls: []*genai.FunctionCall{discardCall("c1", 7)}}}, steps...)}
	approval := NewApproval(a.entries)
	bridge := mustNew(t, librarian(t, llm, approval.Gate(a.ts)), WithApproval(approval), WithThreadStore(store))
	pause := serve(t, bridge, input("run-pause", text("user", "Discard book 7.")))
	if o := outcome(t, pause); o["type"] != "interrupt" {
		t.Fatalf("the first run did not park: %v", o)
	}
	return bridge, pause
}

func approve(runID string) aguix.RunInput {
	in := input(runID, text("user", "Discard book 7."))
	in.Resume = []aguix.ResumeEntry{{InterruptID: "int-c1", Status: "resolved", Payload: json.RawMessage(`{"approved":true}`)}}
	return in
}

func TestTwoResumesOfOneApprovalSentAtOnceRunTheCallOnce(t *testing.T) {
	a, entered, release := slowArchive(t)
	store := newRunMem()
	bridge, _ := parkedBridge(t, a, store, step{text: "Discarded."}, step{text: "Discarded again."})

	first := make(chan served)
	go func() { first <- serve(t, bridge, approve("run-a")) }()
	<-entered
	second := serve(t, bridge, approve("run-b"))
	close(release)
	if done := <-first; done.last()["type"] != "RUN_FINISHED" {
		t.Fatalf("the first resume failed:\n%s", done.body)
	}
	if second.last()["type"] != "RUN_ERROR" || len(second.errs) != 1 || !errors.Is(second.errs[0], errResumeTaken) {
		t.Errorf("the second resume was not refused: %v\n%s", second.errs, second.body)
	}
	if ran := a.ran(); !slices.Equal(ran, []string{"discard 7"}) {
		t.Errorf("ran %v, want the approved call once", ran)
	}
	if row := store.row("thread-1"); row.run != "run-a" || row.status != "idle" {
		t.Errorf("the thread ended %+v", row)
	}
}

func TestAParkedRunStoresItsInterruptsAsRunFinishedSentThem(t *testing.T) {
	store := newRunMem()
	_, pause := parkedBridge(t, newArchive(t), store)
	var finished struct {
		Outcome struct {
			Interrupts json.RawMessage `json:"interrupts"`
		} `json:"outcome"`
	}
	frames := strings.Split(strings.TrimSuffix(pause.body, "\n\n"), "\n\n")
	if err := json.Unmarshal([]byte(strings.TrimPrefix(frames[len(frames)-1], "data: ")), &finished); err != nil {
		t.Fatal(err)
	}
	row := store.row("thread-1")
	if row.status != "waiting" || row.run != "run-pause" || string(row.interrupts) != string(finished.Outcome.Interrupts) ||
		!strings.Contains(string(row.interrupts), `"toolCallId":"c1"`) {
		t.Errorf("stored %+v %s\nRUN_FINISHED carried %s", row, row.interrupts, finished.Outcome.Interrupts)
	}
}

func TestEachRunsEndIsRecorded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		step   step
		status string
		reason string
	}{
		{"success", step{text: "Hi."}, "idle", ""},
		{"failure", step{err: errors.New("model down")}, "failed", "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newRunMem()
			bridge := mustNew(t, librarian(t, &scriptLLM{steps: []step{tc.step}}), WithThreadStore(store))
			serve(t, bridge, input("run-1", text("user", "Hello.")))
			if got := store.ends; len(got) != 1 || got[0].run != "run-1" || got[0].status != tc.status ||
				got[0].reason != tc.reason || got[0].interrupts != nil {
				t.Errorf("ends %+v", got)
			}
		})
	}
}

func TestACancelledRunLeavesTheThreadIdle(t *testing.T) {
	store := newRunMem()
	bridge := mustNew(t, librarian(t, &scriptLLM{steps: []step{{block: true}}}), WithThreadStore(store))
	soon := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 50*time.Millisecond)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	serve(t, bridge, input("run-1", text("user", "Hello.")), soon)
	if got := store.ends; len(got) != 1 || got[0].status != "idle" || got[0].reason != "" {
		t.Errorf("ends %+v", got)
	}
}

func TestRunStoreFailures(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		store := newRunMem()
		store.startErr = errors.New("disk full")
		llm := &scriptLLM{}
		s := serve(t, mustNew(t, librarian(t, llm), WithThreadStore(store)), input("run-1", text("user", "Hello.")))
		if s.last()["type"] != "RUN_ERROR" || len(s.errs) != 1 || !errors.Is(s.errs[0], store.startErr) || llm.calls() != 0 {
			t.Errorf("%v %d:\n%s", s.errs, llm.calls(), s.body)
		}
	})
	t.Run("end of a run that succeeded", func(t *testing.T) {
		store := newRunMem()
		store.endErr = errors.New("disk full")
		s := serve(t, mustNew(t, librarian(t, &scriptLLM{}), WithThreadStore(store)), input("run-1", text("user", "Hello.")))
		if s.last()["type"] != "RUN_ERROR" || len(s.errs) != 1 || !errors.Is(s.errs[0], store.endErr) ||
			!strings.Contains(s.errs[0].Error(), "recording the run's end") {
			t.Errorf("%v:\n%s", s.errs, s.body)
		}
	})
	t.Run("end of a run that failed", func(t *testing.T) {
		store := newRunMem()
		store.endErr = errors.New("disk full")
		model := errors.New("model down")
		s := serve(t, mustNew(t, librarian(t, &scriptLLM{steps: []step{{err: model}}}), WithThreadStore(store)),
			input("run-1", text("user", "Hello.")))
		if len(s.errs) != 1 || !errors.Is(s.errs[0], model) || errors.Is(s.errs[0], store.endErr) {
			t.Errorf("%v", s.errs)
		}
	})
	t.Run("a resume of a thread not waiting", func(t *testing.T) {
		a := newArchive(t)
		store := newRunMem()
		bridge, _ := parkedBridge(t, a, store)
		store.status["thread-1"] = runRow{run: "other", status: "idle"}
		s := serve(t, bridge, approve("run-b"))
		if len(s.errs) != 1 || !errors.Is(s.errs[0], errResumeTaken) || len(a.ran()) != 0 {
			t.Errorf("%v %v", s.errs, a.ran())
		}
	})
}

func TestAStoreThatKeepsNoStatusIsNotAskedTo(t *testing.T) {
	bridge := mustNew(t, librarian(t, &scriptLLM{}), WithThreadStore(newMemStore()))
	if s := serve(t, bridge, input("run-1", text("user", "Hello."))); s.last()["type"] != "RUN_FINISHED" {
		t.Errorf("%s", s.body)
	}
}
