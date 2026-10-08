package runtimehost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pruefwerk/spex/pkg/scheduling"
)

func identity(t *testing.T, mode string) Identity {
	t.Helper()
	root := t.TempDir()
	return Identity{"example.execution/v1", root, "scope-one", filepath.Join(root, "scope.json"), "worker-one", "request-one", mode}
}
func session(t *testing.T, mode string) Session {
	value := identity(t, mode)
	return Session{Identity: value, Directory: filepath.Join(value.Root, "state", value.ScopeName), CleanupSchema: "example.cleanup/v1"}
}
func TestUnscheduledSessionPreservesIdentityAndCompletion(t *testing.T) {
	s := session(t, "disabled")
	cleanups := 0
	s.Cleanup = func(context.Context) error { cleanups++; return nil }
	s.ScheduledOperation = func(context.Context, Operation, string) error {
		t.Fatal("scheduler used without configuration")
		return nil
	}
	ctx := context.Background()
	for _, op := range []Operation{Acquire, Check, Check, Finish} {
		if err := s.Gate(ctx, op); err != nil {
			t.Fatal(op, err)
		}
	}
	if cleanups != 1 {
		t.Fatal(cleanups)
	}
	if s.Gate(ctx, Check) == nil || s.Gate(ctx, Acquire) == nil {
		t.Fatal("finished scope readmitted")
	}
	value, err := ReadIdentity(filepath.Join(s.Directory, "context.json"))
	if err != nil || value != s.Identity {
		t.Fatal("checkpoint encoding changed", value, err)
	}
	info, err := os.Stat(filepath.Join(s.Directory, "context.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("checkpoint permissions", err)
	}
}
func TestConfiguredFailureCannotDowngradeOrRelease(t *testing.T) {
	s := session(t, "enabled")
	calls, cleanups := 0, 0
	s.ScheduledOperation = func(context.Context, Operation, string) error { calls++; return errors.New("SENTINEL_SECRET") }
	s.Cleanup = func(context.Context) error { cleanups++; return nil }
	if err := s.Gate(context.Background(), Acquire); err != ErrAdmission {
		t.Fatal(err)
	}
	s.Identity.Mode = "disabled"
	if s.Gate(context.Background(), Check) != ErrIdentity {
		t.Fatal("mode downgraded")
	}
	if s.Gate(context.Background(), Finish) != ErrAdmission || calls != 2 || cleanups != 1 {
		t.Fatal("cleanup released uncertain capacity", calls, cleanups)
	}
	if _, err := os.Stat(filepath.Join(s.Directory, "cleanup.json")); !os.IsNotExist(err) {
		t.Fatal("false cleanup receipt")
	}
}
func TestSessionRefusesForeignIdentityAndInvalidOperations(t *testing.T) {
	s := session(t, "disabled")
	s.Cleanup = func(context.Context) error { t.Fatal("cleanup used"); return nil }
	if s.Gate(context.Background(), "unknown") != ErrOperation {
		t.Fatal("unknown operation accepted")
	}
	if _, err := os.Stat(s.Directory); !os.IsNotExist(err) {
		t.Fatal("unknown operation wrote state")
	}
	if s.Gate(context.Background(), Acquire) != nil {
		t.Fatal("acquire failed")
	}
	s.Identity.Worker = "other"
	if s.Gate(context.Background(), Finish) != ErrIdentity {
		t.Fatal("foreign worker accepted")
	}
}
func TestFailedCleanupLeavesExecutionActive(t *testing.T) {
	s := session(t, "disabled")
	s.Cleanup = func(context.Context) error { return errors.New("unfinished") }
	ctx := context.Background()
	if s.Gate(ctx, Acquire) != nil || s.Gate(ctx, Finish) != ErrCleanup || s.Gate(ctx, Check) != nil {
		t.Fatal("cleanup changed admission state")
	}
}
func TestMissingAdmissionStillAllowsOwnedRecovery(t *testing.T) {
	s := session(t, "disabled")
	calls := 0
	s.Cleanup = func(context.Context) error { calls++; return nil }
	if s.Gate(context.Background(), Finish) != nil || calls != 1 {
		t.Fatal("partial setup recovery blocked")
	}
}
func TestCancelledAdmissionDoesNotCreateState(t *testing.T) {
	s := session(t, "disabled")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s.Gate(ctx, Acquire) != context.Canceled {
		t.Fatal("cancellation ignored")
	}
	if _, err := os.Stat(s.Directory); !os.IsNotExist(err) {
		t.Fatal("cancelled admission wrote state")
	}
}
func TestCorruptCheckpointStopsBeforeCallbacks(t *testing.T) {
	s := session(t, "enabled")
	if os.MkdirAll(s.Directory, 0700) != nil {
		t.Fatal("fixture directory")
	}
	value := s.Identity
	value.Mode = "invalid"
	if createPrivate(filepath.Join(s.Directory, "context.json"), value) != nil {
		t.Fatal("fixture checkpoint")
	}
	s.Cleanup = func(context.Context) error { t.Fatal("cleanup used"); return nil }
	s.ScheduledOperation = func(context.Context, Operation, string) error { t.Fatal("scheduler used"); return nil }
	if s.Gate(context.Background(), Finish) != ErrIdentity {
		t.Fatal("corrupt checkpoint accepted")
	}
}
func capacity(t *testing.T) (Capacity, *scheduling.Scheduler) {
	value := identity(t, "enabled")
	scheduler := &scheduling.Scheduler{Store: &scheduling.MemoryStore{}, Pool: "example/shared", Capacity: 1}
	c := Capacity{Identity: value, ReportPath: filepath.Join(value.Root, "report.json"), CleanupTimeout: time.Second, ReleaseTimeout: time.Second,
		Open: func(context.Context) (*scheduling.Scheduler, func(), error) { return scheduler, func() {}, nil }}
	return c, scheduler
}
func TestCapacitySpansStepsAndRetainsUnsafeCleanup(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "safe", true: "unsafe"}[fail], func(t *testing.T) {
			c, scheduler := capacity(t)
			c.Cleanup = func(ctx context.Context) error {
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("cleanup unbounded")
				}
				if fail {
					return errors.New("unfinished")
				}
				return nil
			}
			ctx := context.Background()
			for _, op := range []Operation{Acquire, Check, Check} {
				if err := c.Run(ctx, op); err != nil {
					t.Fatal(op, err)
				}
				state, _ := scheduler.Inspect(ctx)
				if len(state.Entries) != 1 || state.Next != 1 {
					t.Fatal("ticket changed between steps", state)
				}
			}
			if err := c.Run(ctx, Finish); (err != nil) != fail {
				t.Fatal(err)
			}
			state, _ := scheduler.Inspect(ctx)
			if fail {
				if len(state.Entries) != 1 || state.Entries[0].Status != "recovery_required" {
					t.Fatal("unsafe slot released", state)
				}
			} else {
				if len(state.Entries) != 0 || c.Run(ctx, Finish) != nil || c.Run(ctx, Check) == nil {
					t.Fatal("completion not idempotent or admission reopened", state)
				}
			}
		})
	}
}
func TestCapacityPersistenceFailureReleasesUnstartedSlot(t *testing.T) {
	c, scheduler := capacity(t)
	c.ReportPath = filepath.Join(c.Identity.Root, "missing", "report.json")
	if c.Run(context.Background(), Acquire) != ErrPersistence {
		t.Fatal("report failure ignored")
	}
	state, _ := scheduler.Inspect(context.Background())
	if len(state.Entries) != 0 {
		t.Fatal("unstarted slot retained", state)
	}
}
func TestUnavailableConfiguredSchedulerFailsClosed(t *testing.T) {
	c, _ := capacity(t)
	c.Open = func(context.Context) (*scheduling.Scheduler, func(), error) {
		return nil, nil, errors.New("SENTINEL_SECRET")
	}
	if c.Run(context.Background(), Acquire) != ErrAdmission {
		t.Fatal("unavailable scheduler accepted")
	}
}

func TestCancelledQueueWaitKeepsTheOtherExecution(t *testing.T) {
	c, scheduler := capacity(t)
	scheduler.PollInterval = time.Millisecond
	other, _, err := scheduler.Acquire(context.Background(), "other-request", "other-worker")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Finish(context.Background(), true)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if !errors.Is(c.Run(ctx, Acquire), context.DeadlineExceeded) {
		t.Fatal("queue cancellation lost")
	}
	state, err := scheduler.Inspect(context.Background())
	if err != nil || len(state.Entries) != 1 || state.Entries[0].Request != "other-request" {
		t.Fatal("cancelled wait changed another execution", state, err)
	}
}

func TestFinishedMarkerCannotBeSilentlyOverwritten(t *testing.T) {
	s := session(t, "disabled")
	s.Cleanup = func(context.Context) error { return nil }
	ctx := context.Background()
	if s.Gate(ctx, Acquire) != nil || s.Gate(ctx, Finish) != nil {
		t.Fatal("fixture lifecycle failed")
	}
	if err := os.WriteFile(filepath.Join(s.Directory, "cleanup.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if s.Gate(ctx, Finish) != ErrIdentity || s.Gate(ctx, Check) == nil {
		t.Fatal("invalid completion receipt accepted")
	}
}
