// Package scenarioruntime defines the in-process runtime boundary. Concrete
// runtimes own configuration resolution, planning and environment lifecycle.
package scenarioruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pruefwerk/spex/pkg/resourceclaims"
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
	Tests                []TestDescription      `json:"tests"`
	ConfigurationSources []string               `json:"configuration_sources"`
	OverridePresent      bool                   `json:"override_present"`
	ResourceClaims       []resourceclaims.Claim `json:"resource_claims,omitempty"`
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
	Schema         string                 `json:"schema"`
	ScenarioID     string                 `json:"scenario_id"`
	Runtime        string                 `json:"runtime"`
	RuntimeRelease string                 `json:"runtime_release,omitempty"`
	Outcome        Outcome                `json:"outcome"`
	Tests          []TestResult           `json:"tests"`
	Artifacts      []string               `json:"artifacts"`
	Cleanup        string                 `json:"cleanup"`
	Problems       []Problem              `json:"problems,omitempty"`
	ResourceClaims *resourceclaims.Report `json:"resource_claims,omitempty"`
	// The runtime must verify application postconditions and any in-flight work,
	// independently of deleting temporary resources, before setting this flag.
	ResourceClaimsSafeToRelease bool `json:"resource_claims_safe_to_release,omitempty"`
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
	mu          sync.RWMutex
	runtimes    map[string]Runtime
	coordinator *resourceclaims.Coordinator
}

type RegistryOption func(*Registry)

func WithResourceClaims(coordinator *resourceclaims.Coordinator) RegistryOption {
	return func(registry *Registry) { registry.coordinator = coordinator }
}

func NewRegistry(options ...RegistryOption) *Registry {
	registry := &Registry{runtimes: map[string]Runtime{}}
	for _, option := range options {
		option(registry)
	}
	return registry
}
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
	Scenario    scenario.Scenario
	Runtime     PreparedRuntime
	Plan        ExecutionPlan
	claims      []resourceclaims.Claim
	coordinator *resourceclaims.Coordinator
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
	if canonical.RuntimeRelease != "" {
		versioned, ok := runtime.(interface{ Release() string })
		if !ok || versioned.Release() != canonical.RuntimeRelease {
			return Prepared{}, &PhaseError{Phase: "runtime selection", cause: errors.New("runtime release is unavailable")}
		}
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
	claims, err := resourceclaims.Normalize(plan.Summary().ResourceClaims)
	if err != nil {
		return Prepared{}, &PhaseError{Phase: "resource claims", cause: err}
	}
	if len(claims) > 0 && (r.coordinator == nil || r.coordinator.Store == nil) {
		return Prepared{}, &PhaseError{Phase: "resource claims", cause: errors.New("resource coordinator is required")}
	}
	return Prepared{Scenario: canonical, Runtime: prepared, Plan: plan, claims: claims, coordinator: r.coordinator}, nil
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
	if p.Scenario.Metadata.Timeout != nil {
		duration, err := time.ParseDuration(*p.Scenario.Metadata.Timeout)
		if err != nil || duration <= 0 {
			return ExecutionResult{}, errors.New("invalid scenario timeout")
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, duration)
		defer cancel()
	}
	result := ExecutionResult{Outcome: Cancelled, Cleanup: "not_run"}
	if len(p.claims) > 0 {
		result.ResourceClaims = &resourceclaims.Report{Claims: append([]resourceclaims.Claim(nil), p.claims...), Status: "not_acquired"}
	}
	if ctx.Err() == nil {
		result, err = p.executeCoordinated(ctx, sink)
	} else {
		err = ctx.Err()
	}
	result.Schema = "spex.result/v1"
	result.ScenarioID = id
	result.Runtime = p.Scenario.Runtime
	result.RuntimeRelease = p.Scenario.RuntimeRelease
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
