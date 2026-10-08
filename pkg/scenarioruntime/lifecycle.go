package scenarioruntime

import (
	"context"
	"errors"
	"time"
)

// Lifecycle owns portable setup and bounded cleanup ordering. The host supplies
// resource-specific operations and public diagnostics; callback errors never
// appear in reports because they may contain credentials.
type Lifecycle struct {
	Setup          func(context.Context) error
	Cleanup        func(context.Context) error
	CleanupTimeout time.Duration
	SetupProblem   Problem
	CleanupProblem Problem
}

// Execute attempts cleanup even when setup fails partway through. It preserves
// test failures and cancellation when cleanup also fails. Call this only after
// validation, planning and pre-execution evidence collection have succeeded.
func (l Lifecycle) Execute(ctx context.Context, execute func(context.Context) (ExecutionResult, error)) (result ExecutionResult, primary error) {
	result = ExecutionResult{Outcome: Error, Cleanup: "not_run"}
	if l.Setup == nil || l.Cleanup == nil || execute == nil || l.CleanupTimeout <= 0 {
		return result, errors.New("invalid environment lifecycle")
	}
	if ctx.Err() != nil {
		result.Outcome = Cancelled
		return result, ctx.Err()
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), l.CleanupTimeout)
		defer cancel()
		if l.Cleanup(cleanup) != nil {
			result.Cleanup = "failed"
			result.Problems = append(result.Problems, l.CleanupProblem)
			if result.Outcome == Passed {
				result.Outcome = Error
			}
			if primary == nil {
				primary = errors.New("environment cleanup incomplete")
			}
		} else if result.Cleanup != "failed" {
			result.Cleanup = "succeeded"
		}
	}()
	if l.Setup(ctx) != nil {
		result.Problems = append(result.Problems, l.SetupProblem)
		if ctx.Err() != nil {
			result.Outcome = Cancelled
			return result, ctx.Err()
		}
		return result, errors.New("environment preparation failed")
	}
	if ctx.Err() != nil {
		result.Outcome = Cancelled
		return result, ctx.Err()
	}
	return execute(ctx)
}
