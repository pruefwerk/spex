package scenarioruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/pruefwerk/spex/pkg/resourceclaims"
)

type claimedPlan struct{ claims []resourceclaims.Claim }

func (p *claimedPlan) Summary() PlanSummary { return PlanSummary{ResourceClaims: p.claims} }

type claimedRuntime struct {
	fake
	plan             *claimedPlan
	coordinator      *resourceclaims.Coordinator
	observedHoldings int
}

func (r *claimedRuntime) Resolve(context.Context, ResolveRequest) (PreparedRuntime, error) {
	r.calls = append(r.calls, "resolve")
	return r, r.resolveErr
}

func (r *claimedRuntime) Plan(context.Context) (ExecutionPlan, error) { return r.plan, nil }
func (r *claimedRuntime) Execute(ctx context.Context, plan ExecutionPlan, sink ArtifactSink) (ExecutionResult, error) {
	holdings, err := r.coordinator.Inspect(ctx)
	if err != nil {
		return ExecutionResult{}, err
	}
	r.observedHoldings = len(holdings)
	return r.fake.Execute(ctx, plan, sink)
}

func claimedFixture(t *testing.T, result ExecutionResult) (*Registry, *claimedRuntime, *resourceclaims.Coordinator) {
	t.Helper()
	c := &resourceclaims.Coordinator{Store: &resourceclaims.MemoryStore{}, PollInterval: time.Millisecond}
	runtime := &claimedRuntime{fake: fake{result: result}, plan: &claimedPlan{[]resourceclaims.Claim{{Resource: "aws/gateway/a", Access: resourceclaims.Exclusive}}}, coordinator: c}
	registry := NewRegistry(WithResourceClaims(c))
	if err := registry.Register(runtime); err != nil {
		t.Fatal(err)
	}
	return registry, runtime, c
}

func TestRuntimeClaimsAcquiredBeforeExecutionAndRecordedInArtifacts(t *testing.T) {
	r, runtime, coordinator := claimedFixture(t, ExecutionResult{Outcome: Passed, Cleanup: "succeeded", ResourceClaimsSafeToRelease: true})
	p, err := r.Prepare(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	sink := &memoryArtifacts{}
	result, err := Run(context.Background(), p, sink)
	if err != nil || runtime.observedHoldings != 1 || result.ResourceClaims.Status != "released" {
		t.Fatal(result, err)
	}
	holdings, _ := coordinator.Inspect(context.Background())
	if len(holdings) != 0 {
		t.Fatal("claims leaked after verified cleanup")
	}
	var plan PlanSummary
	var stored ExecutionResult
	json.Unmarshal(sink.files["plan.json"], &plan)
	json.Unmarshal(sink.files["result.json"], &stored)
	var acquisition resourceclaims.Report
	json.Unmarshal(sink.files["resource-claims.json"], &acquisition)
	if len(plan.ResourceClaims) != 1 || stored.ResourceClaims == nil || stored.ResourceClaims.Status != "released" || acquisition.Owner != stored.ResourceClaims.Owner || acquisition.Status != "acquired" {
		t.Fatal("missing claim evidence")
	}
}

func TestClaimEvidenceFailureDoesNotExecuteAndReleasesClaims(t *testing.T) {
	r, runtime, c := claimedFixture(t, ExecutionResult{})
	p, err := r.Prepare(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), p, &memoryArtifacts{fail: "resource-claims.json"})
	if err == nil || result.Outcome != Error || runtime.observedHoldings != 0 || result.ResourceClaims.Status != "released" {
		t.Fatal("execution started without acquisition evidence", result, err)
	}
	holdings, err := c.Inspect(context.Background())
	if err != nil || len(holdings) != 0 {
		t.Fatal("safe unused claims were retained", err)
	}
}

func TestDeclaredClaimsRequireCoordinatorDuringPreparation(t *testing.T) {
	_, runtime, _ := claimedFixture(t, ExecutionResult{})
	r := NewRegistry()
	r.Register(runtime)
	if _, err := r.Prepare(context.Background(), request()); err == nil {
		t.Fatal("ignored uncoordinated claims")
	}
	if len(runtime.calls) != 1 {
		t.Fatal("execution started without coordinator")
	}
}

func TestCancelledWaitDoesNotExecuteRuntime(t *testing.T) {
	r, runtime, c := claimedFixture(t, ExecutionResult{})
	active, _, err := c.Acquire(context.Background(), runtime.plan.claims)
	if err != nil {
		t.Fatal(err)
	}
	defer active.Finish(context.Background(), true)
	p, err := r.Prepare(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	result, err := p.Execute(ctx, nil)
	if !errors.Is(err, context.DeadlineExceeded) || result.Outcome != Cancelled || result.ResourceClaims.Status != "not_acquired" {
		t.Fatal(result, err)
	}
	if runtime.observedHoldings != 0 {
		t.Fatal("runtime executed while waiting")
	}
}

func TestUncertainRecoveryRetainsClaimsAndPreservesPrimaryFailure(t *testing.T) {
	for _, outcome := range []Outcome{Passed, Failed, Cancelled} {
		t.Run(string(outcome), func(t *testing.T) {
			r, runtime, c := claimedFixture(t, ExecutionResult{Outcome: outcome, Cleanup: "succeeded"})
			if outcome == Failed {
				runtime.executeErr = errors.New("private-backend-error")
			}
			p, err := r.Prepare(context.Background(), request())
			if err != nil {
				t.Fatal(err)
			}
			result, err := p.Execute(context.Background(), nil)
			if err == nil || result.ResourceClaims.Status != "recovery_required" || len(result.Problems) != 1 {
				t.Fatal(result, err)
			}
			if outcome == Passed && result.Outcome != Error || outcome != Passed && result.Outcome != outcome {
				t.Fatal("primary outcome lost", result)
			}
			holdings, _ := c.Inspect(context.Background())
			if len(holdings) != 1 || !holdings[0].RecoveryRequired {
				t.Fatal("unsafe claims released")
			}
		})
	}
}

func TestFailedTemporaryCleanupCannotReleaseClaims(t *testing.T) {
	r, _, c := claimedFixture(t, ExecutionResult{Outcome: Failed, Cleanup: "failed", ResourceClaimsSafeToRelease: true})
	p, err := r.Prepare(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	result, _ := p.Execute(context.Background(), nil)
	if result.ResourceClaims.Status != "recovery_required" {
		t.Fatal(result)
	}
	holdings, _ := c.Inspect(context.Background())
	if len(holdings) != 1 {
		t.Fatal("claims were released")
	}
}

func TestChangedPlanNeverExecutes(t *testing.T) {
	r, runtime, _ := claimedFixture(t, ExecutionResult{})
	p, err := r.Prepare(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	runtime.plan.claims[0].Resource = "unclaimed/gateway/b"
	result, err := p.Execute(context.Background(), nil)
	if err == nil || result.Outcome != Error || runtime.observedHoldings != 0 {
		t.Fatal(result, err)
	}
}
