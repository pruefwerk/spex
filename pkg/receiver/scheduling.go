package receiver

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/pruefwerk/spex/pkg/scenarioruntime"
	"github.com/pruefwerk/spex/pkg/scheduling"
)

// Scheduling is trusted host configuration, not scenario authoring. Verify must
// confirm that execution stopped and capacity cleanup finished. Resource safety
// remains the independent resource coordinator's responsibility.
type Scheduling struct {
	Scheduler *scheduling.Scheduler
	Worker    string
	Verify    func(context.Context, scenarioruntime.ExecutionResult) error
}

// RunScheduled executes a prepared host-managed plan and holds optional capacity
// through verified cleanup. Local hosts and authenticated receivers share this
// implementation; authentication and admission policy remain host-owned.
func RunScheduled(ctx context.Context, prepared scenarioruntime.Prepared, sink scenarioruntime.ArtifactSink, request string, admission *Scheduling) (result scenarioruntime.ExecutionResult, primary error) {
	if admission == nil {
		return scenarioruntime.Run(ctx, prepared, sink)
	}
	if admission.Scheduler == nil || admission.Verify == nil {
		return result, errors.New("receiver scheduler configuration unavailable")
	}
	session, report, err := admission.Scheduler.Acquire(ctx, request, admission.Worker)
	writeReport := func() error {
		data, err := json.Marshal(report)
		if err != nil || sink.Write("scheduling.json", data) != nil {
			return errors.New("receiver scheduling evidence unavailable")
		}
		return nil
	}
	if err != nil {
		_ = writeReport()
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, errors.New("receiver capacity admission failed")
	}
	started := false
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		safe := !started || admission.Verify(cleanup, result) == nil
		report.Status = "released"
		if !safe {
			report.Status = "recovery_required"
		}
		finishErr := session.Finish(cleanup, safe)
		if finishErr != nil {
			report.Status = "unknown"
		}
		evidenceErr := writeReport()
		if result.Schema != "" {
			result.Artifacts = append(result.Artifacts, "scheduling.json")
		}
		if !safe || finishErr != nil || evidenceErr != nil {
			if result.Schema != "" {
				result.Problems = append(result.Problems, scenarioruntime.Problem{Phase: "scheduling", Code: "capacity_release_unconfirmed", Message: "Inspect scheduling evidence and verify worker termination and capacity cleanup before recovery"})
				if result.Outcome == scenarioruntime.Passed {
					result.Outcome = scenarioruntime.Error
				}
			}
			if primary == nil {
				primary = errors.New("receiver capacity release not confirmed")
			}
		}
		if result.Schema != "" {
			data, err := json.Marshal(result)
			if err != nil || sink.Write("result.json", data) != nil {
				if result.Outcome == scenarioruntime.Passed {
					result.Outcome = scenarioruntime.Error
				}
				if primary == nil {
					primary = errors.New("receiver final scheduling result unavailable")
				}
			}
		}
	}()
	if err := writeReport(); err != nil {
		return result, err
	}
	started = true
	return scenarioruntime.Run(ctx, prepared, sink)
}
