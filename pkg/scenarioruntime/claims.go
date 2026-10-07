package scenarioruntime

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/pruefwerk/spex/pkg/resourceclaims"
)

func (p Prepared) executeCoordinated(ctx context.Context, sink ArtifactSink) (ExecutionResult, error) {
	current, err := resourceclaims.Normalize(p.Plan.Summary().ResourceClaims)
	if err != nil || !slices.Equal(current, p.claims) {
		return ExecutionResult{Outcome: Error, Cleanup: "not_run"}, errors.New("resource claims changed after planning")
	}
	if len(p.claims) == 0 {
		return p.Runtime.Execute(ctx, p.Plan, sink)
	}
	session, report, err := p.coordinator.Acquire(ctx, p.claims)
	if err != nil {
		outcome := Error
		if ctx.Err() != nil {
			outcome = Cancelled
		}
		return ExecutionResult{Outcome: outcome, Cleanup: "not_run", ResourceClaims: &report}, err
	}
	// Persist acquisition before environment mutation so a lost worker can be
	// matched to its holding even when it never writes a final result.
	var result ExecutionResult
	var primary error
	if sink != nil {
		data, _ := json.Marshal(report)
		if err := sink.Write("resource-claims.json", data); err != nil {
			result = ExecutionResult{Outcome: Error, Cleanup: "not_run", ResourceClaimsSafeToRelease: true,
				Problems: []Problem{{Phase: "resource claims", Code: "claim_evidence_failed", Message: "Claim acquisition evidence could not be persisted; execution did not start"}}}
			primary = errors.New("cannot persist resource claim acquisition")
		} else {
			result.Artifacts = append(result.Artifacts, "resource-claims.json")
		}
	}
	// Crashes leave durable claims held. Returning from Execute includes the
	// runtime's bounded cleanup; the runtime explicitly confirms safe reuse.
	if primary == nil {
		artifacts := result.Artifacts
		result, primary = p.Runtime.Execute(ctx, p.Plan, sink)
		result.Artifacts = append(result.Artifacts, artifacts...)
	}
	finishCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	safe := result.ResourceClaimsSafeToRelease && (result.Cleanup == "succeeded" || result.Cleanup == "not_run")
	err = session.Finish(finishCtx, safe)
	if err != nil {
		report.Status = "unknown"
		result.Problems = append(result.Problems, Problem{Phase: "resource claims", Code: "claim_finish_failed", Message: "Resource claim disposition could not be confirmed"})
	} else if safe {
		report.Status = "released"
	} else {
		report.Status = "recovery_required"
		result.Problems = append(result.Problems, Problem{Phase: "resource claims", Code: "resource_recovery_required", Message: "Resource claims remain held pending verified recovery"})
	}
	result.ResourceClaims = &report
	if report.Status != "released" {
		if result.Outcome == Passed {
			result.Outcome = Error
		}
		if primary == nil {
			primary = errors.New("resource claims require reconciliation")
		}
	}
	return result, primary
}
