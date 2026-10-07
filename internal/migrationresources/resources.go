package migrationresources

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/pruefwerk/spex/pkg/receiver"
	"github.com/pruefwerk/spex/pkg/resourceclaims"
	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

// ResourceContract comes from the trusted runtime host, not submitted definitions.
// Admission must separately authorize the source commit, operations and targets.
type ResourceContract struct {
	Schema     string                            `json:"schema"`
	ScenarioID string                            `json:"scenario_id"`
	Tests      []scenarioruntime.TestDescription `json:"tests"`
	Claims     []resourceclaims.Claim            `json:"claims"`
}

// SafetyVerifier checks runtime-specific application state and in-flight work.
// Temporary probe cleanup alone does not satisfy this check. The host must
// retain the evidence used by the verifier outside the test environment.
type SafetyVerifier func(context.Context, scenarioruntime.ExecutionResult) error

// WithResourceContract attaches host-resolved physical identities to the existing
// suite runtime. It preserves its configuration resolver, executor and reports.
// Every prepared plan must match this contract; a contract cannot cover another
// scenario or silently omit tests. A nil verifier never permits claim release.
func WithResourceContract(base receiver.Implementation, contract ResourceContract, verify SafetyVerifier) (receiver.Implementation, error) {
	id, err := hex.DecodeString(contract.ScenarioID)
	if base == nil || contract.Schema != "mtb.resource-contract/v1" || err != nil || len(id) != 32 || len(contract.Tests) == 0 {
		return nil, errors.New("invalid runtime resource contract")
	}
	for _, test := range contract.Tests {
		if test.Name == "" || test.Source == "" {
			return nil, errors.New("resource contract requires exact test identities")
		}
	}
	claims, err := resourceclaims.Normalize(contract.Claims)
	if err != nil {
		return nil, err
	}
	contract.Tests = slices.Clone(contract.Tests)
	contract.Claims = claims
	return &resourceRuntime{Implementation: base, contract: contract, verify: verify}, nil
}

type resourceRuntime struct {
	receiver.Implementation
	contract ResourceContract
	verify   SafetyVerifier
}

func (r *resourceRuntime) Resolve(ctx context.Context, request scenarioruntime.ResolveRequest) (scenarioruntime.PreparedRuntime, error) {
	id, err := scenario.Identity(request.Scenario)
	if err != nil || id != r.contract.ScenarioID {
		return nil, errors.New("scenario does not match runtime resource contract")
	}
	prepared, err := r.Implementation.Resolve(ctx, request)
	if err != nil {
		return nil, err
	}
	if prepared == nil {
		return nil, errors.New("runtime returned no prepared configuration")
	}
	return &resourcePrepared{PreparedRuntime: prepared, contract: r.contract, verify: r.verify}, nil
}

type resourcePrepared struct {
	scenarioruntime.PreparedRuntime
	contract ResourceContract
	verify   SafetyVerifier
}
type resourcePlan struct {
	scenarioruntime.ExecutionPlan
	owner   *resourcePrepared
	summary scenarioruntime.PlanSummary
}

func (p resourcePlan) Summary() scenarioruntime.PlanSummary {
	summary := p.summary
	summary.Tests = slices.Clone(summary.Tests)
	summary.ConfigurationSources = slices.Clone(summary.ConfigurationSources)
	summary.ResourceClaims = slices.Clone(summary.ResourceClaims)
	return summary
}

func (r *resourcePrepared) Plan(ctx context.Context) (scenarioruntime.ExecutionPlan, error) {
	plan, err := r.PreparedRuntime.Plan(ctx)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, errors.New("runtime returned no plan")
	}
	summary := plan.Summary()
	if !slices.Equal(summary.Tests, r.contract.Tests) || len(summary.ResourceClaims) != 0 {
		return nil, errors.New("runtime plan does not match resource contract")
	}
	summary.ResourceClaims = slices.Clone(r.contract.Claims)
	summary.ConfigurationSources = append(slices.Clone(summary.ConfigurationSources), "trusted resource contract")
	return resourcePlan{plan, r, summary}, nil
}

func (r *resourcePrepared) Execute(ctx context.Context, plan scenarioruntime.ExecutionPlan, sink scenarioruntime.ArtifactSink) (scenarioruntime.ExecutionResult, error) {
	p, ok := plan.(resourcePlan)
	if !ok || p.owner != r {
		return scenarioruntime.ExecutionResult{Outcome: scenarioruntime.Error, Cleanup: "not_run"}, errors.New("invalid resource execution plan")
	}
	if !slices.Equal(p.ExecutionPlan.Summary().Tests, p.summary.Tests) {
		return scenarioruntime.ExecutionResult{Outcome: scenarioruntime.Error, Cleanup: "not_run", ResourceClaimsSafeToRelease: true}, errors.New("runtime selection changed after resource planning")
	}
	if sink != nil {
		data, _ := json.Marshal(r.contract)
		if sink.Write("resource-contract.json", data) != nil {
			return scenarioruntime.ExecutionResult{Outcome: scenarioruntime.Error, Cleanup: "not_run", ResourceClaimsSafeToRelease: true}, errors.New("cannot persist runtime resource contract; execution did not start")
		}
	}
	result, primary := r.PreparedRuntime.Execute(ctx, p.ExecutionPlan, sink)
	if sink != nil {
		result.Artifacts = append(result.Artifacts, "resource-contract.json")
	}
	// Never inherit an attestation from the underlying suite adapter.
	result.ResourceClaimsSafeToRelease = false
	if len(r.contract.Claims) > 0 && r.verify != nil && (result.Cleanup == "succeeded" || result.Cleanup == "not_run") {
		verification, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := r.verify(verification, result)
		contextErr := verification.Err()
		cancel()
		if err == nil && contextErr == nil {
			result.ResourceClaimsSafeToRelease = true
		} else {
			result.Problems = append(result.Problems, scenarioruntime.Problem{Phase: "resource verification", Code: "resource_postconditions_unverified", Message: "Shared resource safety could not be verified"})
			if result.Outcome == scenarioruntime.Passed {
				result.Outcome = scenarioruntime.Error
			}
			if primary == nil {
				primary = errors.New("shared resource safety unverified")
			}
		}
	}
	return result, primary
}
