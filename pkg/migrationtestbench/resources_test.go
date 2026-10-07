package migrationtestbench

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pruefwerk/spex/pkg/receiver"
	"github.com/pruefwerk/spex/pkg/resourceclaims"
	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

type resourceFixture struct {
	tests    []scenarioruntime.TestDescription
	result   scenarioruntime.ExecutionResult
	executed bool
}

func (f *resourceFixture) ID() string      { return "migration-testbench/v1" }
func (f *resourceFixture) Release() string { return "v1.0.0" }
func (f *resourceFixture) MergeConfig(a, b scenario.RawRuntimeConfig) (scenario.RawRuntimeConfig, error) {
	return a, nil
}
func (f *resourceFixture) Resolve(context.Context, scenarioruntime.ResolveRequest) (scenarioruntime.PreparedRuntime, error) {
	return f, nil
}
func (f *resourceFixture) Plan(context.Context) (scenarioruntime.ExecutionPlan, error) {
	return resourceFixturePlan{f.tests}, nil
}
func (f *resourceFixture) RedactedDescription() any { return struct{}{} }
func (f *resourceFixture) Execute(context.Context, scenarioruntime.ExecutionPlan, scenarioruntime.ArtifactSink) (scenarioruntime.ExecutionResult, error) {
	f.executed = true
	return f.result, nil
}

type resourceFixturePlan struct {
	tests []scenarioruntime.TestDescription
}

func (p resourceFixturePlan) Summary() scenarioruntime.PlanSummary {
	return scenarioruntime.PlanSummary{Tests: p.tests}
}

func resourceRequest(t *testing.T) (scenarioruntime.ResolveRequest, ResourceContract) {
	t.Helper()
	doc := scenario.Scenario{Schema: scenario.Schema, Runtime: "migration-testbench/v1"}
	id, _ := scenario.Identity(doc)
	return scenarioruntime.ResolveRequest{Scenario: doc, Workspace: t.TempDir()}, ResourceContract{Schema: "mtb.resource-contract/v1", ScenarioID: id, Tests: []scenarioruntime.TestDescription{{Name: "migrate", Source: "migration.feature"}}, Claims: []resourceclaims.Claim{{Resource: "aws/dev/gateway/a", Access: resourceclaims.Exclusive}}}
}

func TestResourceContractUsesSpexCoordinatorAndRequiresSafety(t *testing.T) {
	for _, safe := range []bool{true, false} {
		request, contract := resourceRequest(t)
		base := &resourceFixture{tests: contract.Tests, result: scenarioruntime.ExecutionResult{Outcome: scenarioruntime.Passed, Cleanup: "succeeded"}}
		var verifier SafetyVerifier
		if safe {
			verifier = func(context.Context, scenarioruntime.ExecutionResult) error { return nil }
		}
		wrapped, err := WithResourceContract(base, contract, verifier)
		if err != nil {
			t.Fatal(err)
		}
		c := &resourceclaims.Coordinator{Store: &resourceclaims.MemoryStore{}}
		r := scenarioruntime.NewRegistry(scenarioruntime.WithResourceClaims(c))
		r.Register(wrapped)
		prepared, err := r.Prepare(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		result, err := prepared.Execute(context.Background(), nil)
		if !base.executed || (err == nil) != safe || (result.ResourceClaims.Status == "released") != safe {
			t.Fatal(result, err)
		}
		holdings, _ := c.Inspect(context.Background())
		if (len(holdings) == 0) != safe {
			t.Fatal("unsafe claim disposition")
		}
	}
}

func TestResourceContractRejectsChangedSelectionAndScenario(t *testing.T) {
	request, contract := resourceRequest(t)
	base := &resourceFixture{tests: contract.Tests}
	wrapped, _ := WithResourceContract(base, contract, nil)
	request.Scenario.Metadata.Name = "different"
	if _, err := wrapped.Resolve(context.Background(), request); err == nil {
		t.Fatal("accepted different scenario")
	}
	request, _ = resourceRequest(t)
	prepared, err := wrapped.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	base.tests = append(base.tests, scenarioruntime.TestDescription{Name: "uncontracted", Source: "other.feature"})
	if _, err := prepared.Plan(context.Background()); err == nil {
		t.Fatal("accepted uncontracted selection")
	}
}

func TestSafetyVerificationFailurePreservesTestFailure(t *testing.T) {
	request, contract := resourceRequest(t)
	base := &resourceFixture{tests: contract.Tests, result: scenarioruntime.ExecutionResult{Outcome: scenarioruntime.Failed, Cleanup: "succeeded", ResourceClaimsSafeToRelease: true}}
	wrapped, _ := WithResourceContract(base, contract, func(context.Context, scenarioruntime.ExecutionResult) error { return errors.New("SECRET") })
	prepared, _ := wrapped.Resolve(context.Background(), request)
	plan, _ := prepared.Plan(context.Background())
	result, err := prepared.Execute(context.Background(), plan, nil)
	if err == nil || err.Error() == "SECRET" || result.Outcome != scenarioruntime.Failed || result.ResourceClaimsSafeToRelease {
		t.Fatal(result, err)
	}
}

func TestPrivateStackContractPreservesExistingRuntimePlan(t *testing.T) {
	root, _ := filepath.Abs("../..")
	t.Chdir(root)
	base, err := New(Config{RepositoryRoot: root, SuitePath: "examples/suites/mqtt-local.yaml", Release: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	doc := scenario.Scenario{Schema: scenario.Schema, Runtime: base.ID()}
	request := scenarioruntime.ResolveRequest{Scenario: doc, Workspace: root}
	prepared, err := base.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := prepared.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := scenario.Identity(doc)
	contract := ResourceContract{Schema: "mtb.resource-contract/v1", ScenarioID: id, Tests: plan.Summary().Tests}
	wrapped, err := WithResourceContract(base, contract, nil)
	if err != nil {
		t.Fatal(err)
	}
	var _ receiver.Implementation = wrapped
	other, err := wrapped.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	otherPlan, err := other.Plan(context.Background())
	if err != nil || !reflect.DeepEqual(plan.Summary().Tests, otherPlan.Summary().Tests) || len(otherPlan.Summary().ResourceClaims) != 0 {
		t.Fatal("private-stack plan changed", err)
	}
}
