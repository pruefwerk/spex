package scenarioruntime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/pkg/scenario"
)

type fake struct {
	calls                           []string
	resolveErr, planErr, executeErr error
	result                          ExecutionResult
	cancel                          context.CancelFunc
}

func (f *fake) ID() string { return "test/v1" }
func (f *fake) Resolve(ctx context.Context, r ResolveRequest) (PreparedRuntime, error) {
	f.calls = append(f.calls, "resolve")
	return f, f.resolveErr
}
func (f *fake) Plan(ctx context.Context) (ExecutionPlan, error) {
	f.calls = append(f.calls, "plan")
	if f.cancel != nil {
		f.cancel()
	}
	return testPlan{}, f.planErr
}
func (f *fake) Execute(ctx context.Context, p ExecutionPlan, a ArtifactSink) (ExecutionResult, error) {
	f.calls = append(f.calls, "execute", "cleanup")
	return f.result, f.executeErr
}
func (f *fake) RedactedDescription() any { return struct{}{} }

type testPlan struct{}

func (testPlan) Summary() PlanSummary { return PlanSummary{} }
func request() ResolveRequest {
	return ResolveRequest{Scenario: scenario.Scenario{Schema: scenario.Schema, Runtime: "test/v1"}}
}

func TestLifecycleOrdering(t *testing.T) {
	f := &fake{result: ExecutionResult{Outcome: Passed, Cleanup: "succeeded"}}
	r := NewRegistry()
	if err := r.Register(f); err != nil {
		t.Fatal(err)
	}
	p, err := r.Prepare(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, []string{"resolve", "plan"}) {
		t.Fatal(f.calls)
	}
	result, err := p.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, []string{"resolve", "plan", "execute", "cleanup"}) || result.Schema != "spex.result/v1" || len(result.ScenarioID) != 64 {
		t.Fatal("invalid lifecycle/result")
	}
}

func TestValidationAndPlanningFailuresNeverExecute(t *testing.T) {
	for _, phase := range []string{"validation", "runtime", "resolve", "plan"} {
		t.Run(phase, func(t *testing.T) {
			f := &fake{}
			r := NewRegistry()
			_ = r.Register(f)
			req := request()
			switch phase {
			case "validation":
				req.Scenario.Schema = "wrong"
			case "runtime":
				req.Scenario.Runtime = "missing/v1"
			case "resolve":
				f.resolveErr = errors.New("secret-sentinel")
			case "plan":
				f.planErr = errors.New("secret-sentinel")
			}
			_, err := r.Prepare(context.Background(), req)
			if err == nil || strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatal("missing/unsafe error")
			}
			if strings.Contains(strings.Join(f.calls, ","), "execute") {
				t.Fatal("executed invalid scenario")
			}
		})
	}
}

func TestCancellationStopsAtPhaseBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fake{cancel: cancel}
	r := NewRegistry()
	_ = r.Register(f)
	_, err := r.Prepare(ctx, request())
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, []string{"resolve", "plan"}) {
		t.Fatal(f.calls)
	}
	f.cancel = nil
	f.calls = nil
	p, err := r.Prepare(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Execute(ctx, nil)
	if result.Outcome != Cancelled || !errors.Is(err, context.Canceled) {
		t.Fatal(result, err)
	}
	if !reflect.DeepEqual(f.calls, []string{"resolve", "plan"}) {
		t.Fatal("execute ran after cancellation")
	}
}

func TestPrimaryFailureSurvivesCleanupFailure(t *testing.T) {
	f := &fake{result: ExecutionResult{Outcome: Failed, Cleanup: "failed", Problems: []Problem{{Phase: "cleanup", Code: "cleanup_failed", Message: "cleanup failed"}}}, executeErr: errors.New("secret-sentinel")}
	r := NewRegistry()
	_ = r.Register(f)
	p, err := r.Prepare(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Execute(context.Background(), nil)
	if result.Outcome != Failed || result.Cleanup != "failed" || len(result.Problems) != 1 || err == nil || strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatal(result, err)
	}
}

func TestRegistrationAndRuntimeSelection(t *testing.T) {
	r := NewRegistry()
	f := &fake{}
	if err := r.Register(f); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(f); err == nil {
		t.Fatal("accepted duplicate runtime")
	}
	if _, err := r.Lookup(" TEST/V1 "); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Lookup("other/v1"); err == nil {
		t.Fatal("accepted unknown runtime")
	}
}
