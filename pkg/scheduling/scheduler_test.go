package scheduling

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func scheduler(capacity int) *Scheduler {
	return &Scheduler{Store: &MemoryStore{}, Pool: "kind/runner-pool", Capacity: capacity, PollInterval: time.Millisecond}
}

func awaitQueued(t *testing.T, s *Scheduler, count int) State {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state, err := s.Inspect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Entries) == count {
			return state
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("queue did not reach expected size")
	return State{}
}

func TestFIFOAndCapacityAcrossIndependentSchedulers(t *testing.T) {
	s := scheduler(1)
	first, _, err := s.Acquire(context.Background(), "first", "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	second := *s
	order := make(chan string, 2)
	finished := make(chan error, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for index, name := range []string{"second", "third"} {
		go func() {
			worker := second
			if name == "second" {
				worker.PollInterval = 30 * time.Millisecond
			}
			lease, _, err := worker.Acquire(ctx, name, "worker-"+name)
			if err == nil {
				order <- name
				err = lease.Finish(ctx, true)
			}
			finished <- err
		}()
		awaitQueued(t, s, index+2)
	}
	if err := first.Finish(ctx, true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"second", "third"} {
		if actual := <-order; actual != name {
			t.Fatal("not FIFO", actual)
		}
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
}

type uncertainStart struct {
	memory MemoryStore
	calls  int
}

func (s *uncertainStart) Transaction(ctx context.Context, action func(*State) error) error {
	s.calls++
	err := s.memory.Transaction(ctx, action)
	if err == nil && s.calls == 2 {
		return ErrStore
	}
	return err
}

func TestLostAdmissionAcknowledgementRetainsCapacity(t *testing.T) {
	store := &uncertainStart{}
	s := scheduler(1)
	s.Store = store
	lease, report, err := s.Acquire(context.Background(), "request", "worker")
	if lease != nil || err == nil || report.Status != "unknown" {
		t.Fatal(report, err)
	}
	state, err := s.Inspect(context.Background())
	if err != nil || len(state.Entries) != 1 || state.Entries[0].Owner != report.Owner || state.Entries[0].Status != "running" {
		t.Fatal("uncertain slot was lost", state, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, _, err := s.Acquire(ctx, "other", "other-worker"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("uncertain capacity was reused", err)
	}
}

func TestCancellationWithdrawsOnlyWaitingRequest(t *testing.T) {
	s := scheduler(1)
	first, _, _ := s.Acquire(context.Background(), "first", "worker")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Report, 1)
	go func() {
		_, report, err := s.Acquire(ctx, "second", "waiting")
		if !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
		done <- report
	}()
	awaitQueued(t, s, 2)
	cancel()
	if report := <-done; report.Status != "cancelled" {
		t.Fatal(report)
	}
	state := awaitQueued(t, s, 1)
	if state.Entries[0].Status != "running" {
		t.Fatal(state)
	}
	if err := first.Finish(context.Background(), true); err != nil {
		t.Fatal(err)
	}
}

type cancelAdmissionStore struct {
	memory MemoryStore
	cancel context.CancelFunc
	calls  int
	commit bool
}

func (s *cancelAdmissionStore) Transaction(ctx context.Context, action func(*State) error) error {
	s.calls++
	if s.calls == 2 {
		if s.commit {
			if err := s.memory.Transaction(ctx, action); err != nil {
				return err
			}
		}
		s.cancel()
		return ctx.Err()
	}
	return s.memory.Transaction(ctx, action)
}

func TestCancellationDuringAdmissionReconcilesQueuedAndCommittedSlots(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprint(commit), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &cancelAdmissionStore{cancel: cancel, commit: commit}
			s := scheduler(1)
			s.Store = store
			lease, report, err := s.Acquire(ctx, "request", "worker")
			if lease != nil || !errors.Is(err, context.Canceled) {
				t.Fatal(lease, report, err)
			}
			state, err := s.Inspect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !commit && (report.Status != "cancelled" || len(state.Entries) != 0) {
				t.Fatal("cancelled queue entry was not withdrawn", report, state)
			}
			if commit && (report.Status != "unknown" || len(state.Entries) != 1 || state.Entries[0].Status != "running") {
				t.Fatal("uncertain admission lost its reserved slot", report, state)
			}
		})
	}
}

func TestUnverifiedSlotConsumesCapacityUntilExplicitRecovery(t *testing.T) {
	s := scheduler(1)
	lease, report, _ := s.Acquire(context.Background(), "first", "worker")
	if err := lease.Finish(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := s.Acquire(ctx, "second", "other"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	for _, proof := range []Recovery{{}, {true, false, "receipt"}, {false, true, "receipt"}, {true, true, ""}} {
		if _, err := s.Recover(context.Background(), report.Owner, proof); err == nil {
			t.Fatal("accepted incomplete recovery")
		}
	}
	entry, err := s.Recover(context.Background(), report.Owner, Recovery{true, true, "verified-worker-and-ledger"})
	if err != nil || entry.Status != "recovery_required" {
		t.Fatal(entry, err)
	}
	if _, _, err := s.Acquire(context.Background(), "third", "worker-3"); err != nil {
		t.Fatal(err)
	}
}

func TestCapacityDoesNotExceedConfiguredLimit(t *testing.T) {
	s := scheduler(3)
	var active, maximum atomic.Int64
	var workers sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := range 30 {
		workers.Go(func() {
			lease, _, err := s.Acquire(ctx, fmt.Sprint(i), "worker")
			if err != nil {
				t.Error(err)
				return
			}
			count := active.Add(1)
			for old := maximum.Load(); count > old && !maximum.CompareAndSwap(old, count); old = maximum.Load() {
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			if err := lease.Finish(ctx, true); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	if maximum.Load() > 3 {
		t.Fatal("capacity exceeded", maximum.Load())
	}
	state, err := s.Inspect(ctx)
	if err != nil || len(state.Entries) != 0 || state.Next != 30 {
		t.Fatal(state, err)
	}
}

func TestDuplicateConfigurationDriftAndUnknownWorkerFailClosed(t *testing.T) {
	s := scheduler(1)
	_, report, _ := s.Acquire(context.Background(), "request", "worker")
	if _, _, err := s.Acquire(context.Background(), "request", "other"); !errors.Is(err, ErrDuplicate) {
		t.Fatal(err)
	}
	other := *s
	other.Capacity = 2
	if _, _, err := other.Acquire(context.Background(), "other", "worker"); !errors.Is(err, ErrStore) {
		t.Fatal(err)
	}
	if _, err := s.Recover(context.Background(), report.Owner+"bad", Recovery{true, true, "proof"}); !errors.Is(err, ErrOwnership) {
		t.Fatal(err)
	}
	state, _ := s.Inspect(context.Background())
	if len(state.Entries) != 1 || state.Entries[0].Status != "running" {
		t.Fatal(state)
	}
}

type unavailable struct{}

func (unavailable) Transaction(context.Context, func(*State) error) error { return ErrStore }

func TestStoreFailureAndCorruptionNeverAdmit(t *testing.T) {
	s := scheduler(1)
	s.Store = unavailable{}
	if lease, report, err := s.Acquire(context.Background(), "request", "worker"); lease != nil || report.Status != "unknown" || err == nil {
		t.Fatal(report, err)
	}
	for _, data := range []string{"", "{", `{"unexpected":true}`, `{"entries":[]} {}`, `{"pool":"kind","capacity":0}`, `{"pool":"kind","capacity":1,"next":1,"entries":[{"owner":"bad","request":"r","worker":"w","sequence":1,"status":"running"}]}`} {
		if _, err := decode([]byte(data)); err == nil {
			t.Fatal("accepted corrupt state")
		}
	}
	if _, err := NewMongoStore(nil, "kind"); err == nil {
		t.Fatal("accepted missing collection")
	}
}

func TestResumeBindsCompleteTicketAndRefusesRecoveryOrReleasedSlots(t *testing.T) {
	s := scheduler(1)
	lease, report, err := s.Acquire(context.Background(), "request", "worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Report){func(r *Report) { r.Worker = "other" }, func(r *Report) { r.Request = "other" }, func(r *Report) { r.Pool = "other" }, func(r *Report) { r.Capacity = 2 }, func(r *Report) { r.Sequence++ }, func(r *Report) { r.Status = "queued" }} {
		copy := report
		mutate(&copy)
		if _, err := s.Resume(context.Background(), copy); err == nil {
			t.Fatal("accepted mismatched ticket")
		}
	}
	if _, err := s.Resume(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	if err := lease.Finish(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resume(context.Background(), report); err == nil {
		t.Fatal("resumed recovery-required slot")
	}
	if _, err := s.Recover(context.Background(), report.Owner, Recovery{true, true, "proof"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resume(context.Background(), report); err == nil {
		t.Fatal("resumed released slot")
	}
}
