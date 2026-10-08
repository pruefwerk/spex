package scenarioruntime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLifecyclePreservesPrimaryOutcome(t *testing.T) {
	for _, outcome := range []Outcome{Passed, Failed, Cancelled} {
		for _, cleanupFails := range []bool{false, true} {
			calls := []string{}
			primary := errors.New("test failure")
			l := Lifecycle{
				Setup: func(context.Context) error { calls = append(calls, "setup"); return nil },
				Cleanup: func(ctx context.Context) error {
					calls = append(calls, "cleanup")
					if _, ok := ctx.Deadline(); !ok {
						t.Fatal("cleanup has no deadline")
					}
					if cleanupFails {
						return errors.New("SECRET_SENTINEL")
					}
					return nil
				},
				CleanupTimeout: time.Second,
				CleanupProblem: Problem{Phase: "cleanup", Code: "incomplete", Message: "Cleanup incomplete"},
			}
			result, err := l.Execute(context.Background(), func(context.Context) (ExecutionResult, error) {
				calls = append(calls, "execute")
				if outcome == Passed {
					return ExecutionResult{Outcome: outcome}, nil
				}
				return ExecutionResult{Outcome: outcome}, primary
			})
			want := outcome
			if cleanupFails && outcome == Passed {
				want = Error
			}
			if result.Outcome != want || !reflect.DeepEqual(calls, []string{"setup", "execute", "cleanup"}) {
				t.Fatal(result, calls)
			}
			if outcome != Passed && err != primary {
				t.Fatal("cleanup replaced primary error")
			}
			if err != nil && strings.Contains(err.Error(), "SECRET_SENTINEL") {
				t.Fatal("callback error leaked")
			}
		}
	}
}

func TestLifecycleCancellationDuringSetupStillCleans(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cleaned := false
	l := Lifecycle{Setup: func(context.Context) error { cancel(); return nil },
		Cleanup: func(ctx context.Context) error { cleaned = true; return ctx.Err() }, CleanupTimeout: time.Second}
	result, err := l.Execute(ctx, func(context.Context) (ExecutionResult, error) {
		t.Fatal("executed after cancellation")
		return ExecutionResult{}, nil
	})
	if !cleaned || result.Outcome != Cancelled || !errors.Is(err, context.Canceled) || result.Cleanup != "succeeded" {
		t.Fatal(result, err)
	}
}

func TestLifecycleSetupFailureAndPreflight(t *testing.T) {
	cleaned := false
	l := Lifecycle{Setup: func(context.Context) error { return errors.New("SECRET_SENTINEL") },
		Cleanup: func(context.Context) error { cleaned = true; return nil }, CleanupTimeout: time.Second}
	result, err := l.Execute(context.Background(), func(context.Context) (ExecutionResult, error) {
		t.Fatal("executed after setup failed")
		return ExecutionResult{}, nil
	})
	if !cleaned || result.Outcome != Error || err == nil || strings.Contains(err.Error(), "SECRET_SENTINEL") {
		t.Fatal(result, err)
	}
	cleaned = false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l.Setup = func(context.Context) error { t.Fatal("setup started after cancellation"); return nil }
	result, err = l.Execute(ctx, func(context.Context) (ExecutionResult, error) {
		t.Fatal("execution started after cancellation")
		return ExecutionResult{}, nil
	})
	if cleaned || result.Cleanup != "not_run" || !errors.Is(err, context.Canceled) {
		t.Fatal(result, err)
	}
}

func TestLifecycleKeepsInnerCleanupFailure(t *testing.T) {
	l := Lifecycle{Setup: func(context.Context) error { return nil },
		Cleanup: func(context.Context) error { return nil }, CleanupTimeout: time.Second}
	result, err := l.Execute(context.Background(), func(context.Context) (ExecutionResult, error) {
		return ExecutionResult{Outcome: Failed, Cleanup: "failed"}, nil
	})
	if err != nil || result.Cleanup != "failed" || result.Outcome != Failed {
		t.Fatal(result, err)
	}
}
