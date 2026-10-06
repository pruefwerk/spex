// Package scenarioruntime defines the in-process runtime boundary. Concrete
// runtimes own configuration resolution, planning and environment lifecycle.
package scenarioruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/pruefwerk/spex/pkg/scenario"
)

type ResolveRequest struct {
	Scenario  scenario.Scenario
	Workspace string
}

type Runtime interface {
	ID() string
	Resolve(context.Context, ResolveRequest) (PreparedRuntime, error)
}

type ConfigAuthor interface {
	MergeConfig(scenario.RawRuntimeConfig, scenario.RawRuntimeConfig) (scenario.RawRuntimeConfig, error)
}

type PreparedRuntime interface {
	Plan(context.Context) (ExecutionPlan, error)
	Execute(context.Context, ExecutionPlan, ArtifactSink) (ExecutionResult, error)
	RedactedDescription() any
}

// ExecutionPlan keeps runtime-owned typed state out of the registry. Only its
// safe summary is serialized; a plan is never deserialized into executable code.
type ExecutionPlan interface{ Summary() PlanSummary }
type PlanSummary struct {
	Tests                []TestDescription `json:"tests"`
	ConfigurationSources []string          `json:"configuration_sources"`
	OverridePresent      bool              `json:"override_present"`
}
type TestDescription struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

type ArtifactSink interface {
	Directory() string
	Write(string, []byte) error
}

type Outcome string

const (
	Passed    Outcome = "passed"
	Failed    Outcome = "failed"
	Error     Outcome = "error"
	Cancelled Outcome = "cancelled"
)

type Problem struct {
	Phase   string `json:"phase"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
type TestResult struct {
	Name    string  `json:"name"`
	Outcome Outcome `json:"outcome"`
}
type ExecutionResult struct {
	Schema     string       `json:"schema"`
	ScenarioID string       `json:"scenario_id"`
	Runtime    string       `json:"runtime"`
	Outcome    Outcome      `json:"outcome"`
	Tests      []TestResult `json:"tests"`
	Artifacts  []string     `json:"artifacts"`
	Cleanup    string       `json:"cleanup"`
	Problems   []Problem    `json:"problems,omitempty"`
}

// PhaseError deliberately omits the underlying error text: configuration and
// command errors can contain secrets. Unwrap preserves cancellation detection.
type PhaseError struct {
	Phase string
	cause error
}

func (e *PhaseError) Error() string { return "scenario " + e.Phase + " failed" }
func (e *PhaseError) Unwrap() error { return e.cause }

type Registry struct {
	mu       sync.RWMutex
	runtimes map[string]Runtime
}

func NewRegistry() *Registry { return &Registry{runtimes: map[string]Runtime{}} }
func (r *Registry) Register(runtime Runtime) error {
	if runtime == nil {
		return errors.New("runtime is required")
	}
	id := runtime.ID()
	if err := scenario.ValidateGeneric(scenario.Scenario{Schema: scenario.Schema, Runtime: id}); err != nil || id != scenario.RuntimeID(id) {
		return errors.New("invalid runtime registration")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runtimes == nil {
		r.runtimes = map[string]Runtime{}
	}
	if _, found := r.runtimes[id]; found {
		return errors.New("runtime already registered")
	}
	r.runtimes[id] = runtime
	return nil
}
func (r *Registry) Lookup(id string) (Runtime, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	runtime, ok := r.runtimes[scenario.RuntimeID(id)]
	if !ok {
		return nil, errors.New("unknown runtime")
	}
	return runtime, nil
}

type Prepared struct {
	Scenario scenario.Scenario
	Runtime  PreparedRuntime
	Plan     ExecutionPlan
}

// Prepare enforces validation and lifecycle order without executing any tests.
func (r *Registry) Prepare(ctx context.Context, request ResolveRequest) (Prepared, error) {
	if err := ctx.Err(); err != nil {
		return Prepared{}, &PhaseError{Phase: "cancelled", cause: err}
	}
	canonical, err := scenario.Canonicalize(request.Scenario)
	if err != nil {
		return Prepared{}, &PhaseError{Phase: "validation", cause: err}
	}
	runtime, err := r.Lookup(canonical.Runtime)
	if err != nil {
		return Prepared{}, &PhaseError{Phase: "runtime selection", cause: err}
	}
	if err = scenario.ValidateSources(canonical, request.Workspace); err != nil {
		return Prepared{}, &PhaseError{Phase: "source validation", cause: err}
	}
	request.Scenario = canonical
	prepared, err := runtime.Resolve(ctx, request)
	if err != nil {
		return Prepared{}, &PhaseError{Phase: "configuration", cause: err}
	}
	if prepared == nil {
		return Prepared{}, errors.New("runtime returned no prepared configuration")
	}
	if err = ctx.Err(); err != nil {
		return Prepared{}, &PhaseError{Phase: "cancelled", cause: err}
	}
	plan, err := prepared.Plan(ctx)
	if err != nil {
		return Prepared{}, &PhaseError{Phase: "planning", cause: err}
	}
	if plan == nil {
		return Prepared{}, errors.New("runtime returned no execution plan")
	}
	if err = ctx.Err(); err != nil {
		return Prepared{}, &PhaseError{Phase: "cancelled", cause: err}
	}
	return Prepared{Scenario: canonical, Runtime: prepared, Plan: plan}, nil
}

// Execute preserves a test failure when evidence collection or cleanup also
// fails. Concrete runtimes attempt bounded cleanup and report secondary problems.
func (p Prepared) Execute(ctx context.Context, sink ArtifactSink) (ExecutionResult, error) {
	id, err := scenario.Identity(p.Scenario)
	if err != nil {
		return ExecutionResult{}, err
	}
	if p.Runtime == nil || p.Plan == nil {
		return ExecutionResult{}, errors.New("scenario was not prepared")
	}
	result := ExecutionResult{Outcome: Cancelled, Cleanup: "not_run"}
	if ctx.Err() == nil {
		result, err = p.Runtime.Execute(ctx, p.Plan, sink)
	} else {
		err = ctx.Err()
	}
	result.Schema = "spex.result/v1"
	result.ScenarioID = id
	result.Runtime = p.Scenario.Runtime
	if result.Tests == nil {
		result.Tests = []TestResult{}
	}
	if result.Artifacts == nil {
		result.Artifacts = []string{}
	}
	if ctx.Err() != nil && (result.Outcome == "" || result.Outcome == Passed) {
		result.Outcome = Cancelled
	}
	if result.Outcome == "" || (result.Outcome == Passed && err != nil) {
		result.Outcome = Error
	}
	switch result.Outcome {
	case Passed, Failed, Error, Cancelled:
	default:
		return result, errors.New("runtime returned invalid outcome")
	}
	if err != nil {
		return result, &PhaseError{Phase: "execution", cause: err}
	}
	if result.Outcome != Passed {
		return result, fmt.Errorf("scenario %s", result.Outcome)
	}
	return result, nil
}
