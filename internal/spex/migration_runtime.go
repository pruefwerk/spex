package spex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/pruefwerk/spex/internal/workspace"
	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

// migrationRuntime adapts the existing suite resolver and executor. The caller
// supplies repository configuration through the same flags as the legacy CLI;
// the adapter carries no service topology or company-specific defaults.
type migrationRuntime struct {
	flags suiteFlags
}

func (migrationRuntime) ID() string { return "migration-testbench/v1" }

func (r migrationRuntime) Resolve(ctx context.Context, request scenarioruntime.ResolveRequest) (scenarioruntime.PreparedRuntime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	overlay, err := decodeMigrationOverlay(request.Scenario.RuntimeConfig)
	if err != nil {
		return nil, err
	}
	if len(request.Scenario.Tests) != 0 {
		return nil, errors.New("explicit runtime sources are not enabled yet")
	}
	flags := r.flags
	if overlay.Suite != nil {
		flags.suitePath, err = scenario.SourcePath(request.Workspace, *overlay.Suite)
		if err != nil {
			return nil, errors.New("runtime suite is unavailable or outside workspace")
		}
	}
	if flags.suitePath == "" {
		return nil, errors.New("runtime requires an existing suite selection")
	}
	if !filepath.IsAbs(flags.suitePath) {
		flags.suitePath = filepath.Join(request.Workspace, flags.suitePath)
	}
	resolved, err := workspace.LoadScenarioSuite(flags.suitePath)
	if err != nil {
		return nil, errors.New("could not resolve runtime suite")
	}
	inputs, err := loadSuiteInputs(resolved, flags)
	if err != nil {
		return nil, errors.New("could not resolve runtime inputs")
	}
	resolved, inputs, flags, err = overlay.Apply(resolved, inputs, flags)
	if err != nil {
		return nil, err
	}
	return &preparedMigrationRuntime{resolved: resolved, inputs: inputs, flags: flags, overridePresent: !request.Scenario.RuntimeConfig.Empty()}, nil
}

type preparedMigrationRuntime struct {
	resolved        workspace.ResolvedScenarioSuite
	inputs          []workspace.Inputs
	flags           suiteFlags
	overridePresent bool
}

type migrationExecutionPlan struct {
	owner   *preparedMigrationRuntime
	summary scenarioruntime.PlanSummary
}

func (p migrationExecutionPlan) Summary() scenarioruntime.PlanSummary {
	summary := p.summary
	summary.Tests = append([]scenarioruntime.TestDescription(nil), summary.Tests...)
	summary.ConfigurationSources = append([]string(nil), summary.ConfigurationSources...)
	return summary
}

func (r *preparedMigrationRuntime) Plan(ctx context.Context) (scenarioruntime.ExecutionPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	inputs, err := suiteRunInputs(r.resolved, r.inputs)
	if err != nil {
		return nil, err
	}
	summary := scenarioruntime.PlanSummary{ConfigurationSources: []string{"suite"}}
	if r.resolved.BindingPath != "" {
		summary.ConfigurationSources = append(summary.ConfigurationSources, "target binding")
	}
	if len(r.resolved.CatalogPaths) > 0 {
		summary.ConfigurationSources = append(summary.ConfigurationSources, "catalogs")
	}
	for _, input := range inputs {
		if input.Integration != nil {
			summary.ConfigurationSources = append(summary.ConfigurationSources, "integration profile")
			break
		}
	}
	summary.OverridePresent = r.overridePresent
	for _, input := range inputs {
		summary.Tests = append(summary.Tests, scenarioruntime.TestDescription{Name: input.ScenarioName, Source: filepath.Base(input.ScenarioPath)})
	}
	return migrationExecutionPlan{owner: r, summary: summary}, nil
}

func (r *preparedMigrationRuntime) RedactedDescription() any {
	return struct {
		Runtime string `json:"runtime"`
		Version string `json:"version"`
	}{"migration-testbench/v1", Version}
}

func (r *preparedMigrationRuntime) Execute(ctx context.Context, plan scenarioruntime.ExecutionPlan, sink scenarioruntime.ArtifactSink) (scenarioruntime.ExecutionResult, error) {
	result := scenarioruntime.ExecutionResult{Outcome: scenarioruntime.Error, Cleanup: "not_run"}
	p, ok := plan.(migrationExecutionPlan)
	if !ok || p.owner != r || sink == nil {
		return result, errors.New("invalid runtime execution plan or artifact sink")
	}
	if err := ctx.Err(); err != nil {
		result.Outcome = scenarioruntime.Cancelled
		return result, err
	}
	// A cancellable context also enables the bounded cleanup path, even when
	// the caller has not supplied a deadline.
	executionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	flags := r.flags
	flags.ctx = executionCtx
	flags.outPath = filepath.Join(sink.Directory(), "artifacts")
	err := runResolvedSuite(r.resolved, r.inputs, flags, io.Discard, io.Discard)
	result = migrationExecutionEvidence(flags.outPath, len(p.summary.Tests), flags.retainRuntime)
	if ctx.Err() != nil {
		if result.Outcome != scenarioruntime.Failed {
			result.Outcome = scenarioruntime.Cancelled
		}
		return result, ctx.Err()
	}
	if err != nil {
		if result.Outcome == scenarioruntime.Passed {
			result.Outcome = scenarioruntime.Error
		}
		result.Problems = append(result.Problems, scenarioruntime.Problem{Phase: "execution", Code: "suite_execution_failed", Message: "Inspect suite evidence"})
		return result, errors.New("runtime suite execution failed")
	}
	return result, nil
}

func migrationExecutionEvidence(root string, expected int, retained bool) scenarioruntime.ExecutionResult {
	result := scenarioruntime.ExecutionResult{Outcome: scenarioruntime.Passed, Cleanup: "succeeded", Artifacts: []string{"artifacts"}}
	paths, _ := filepath.Glob(filepath.Join(root, "*", "reports", "scenario-run-report.json"))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		var report ScenarioRunReport
		if err != nil || json.Unmarshal(content, &report) != nil {
			continue
		}
		outcome := scenarioruntime.Error
		switch report.Status.ScenarioResult {
		case "passed":
			if report.Status.RunnerResult == "passed" {
				outcome = scenarioruntime.Passed
			}
		case "failed":
			// A failed KUTTL process alone does not establish a failed
			// assertion. Require a mapped non-setup operation failure.
			for _, step := range report.Steps {
				if step.Result == "failed" && !step.Internal {
					outcome = scenarioruntime.Failed
				}
			}
		}
		if report.Status.FailureClass != nil && *report.Status.FailureClass == "runtime_cleanup_failed" {
			result.Cleanup = "failed"
			result.Problems = append(result.Problems, scenarioruntime.Problem{Phase: "cleanup", Code: "runtime_cleanup_failed", Message: "Runtime resource cleanup failed"})
		}
		result.Tests = append(result.Tests, scenarioruntime.TestResult{Name: report.Metadata.Name, Outcome: outcome})
		if outcome == scenarioruntime.Failed || (outcome == scenarioruntime.Error && result.Outcome == scenarioruntime.Passed) {
			result.Outcome = outcome
		}
	}
	if len(result.Tests) != expected {
		if result.Outcome == scenarioruntime.Passed {
			result.Outcome = scenarioruntime.Error
		}
		if result.Cleanup != "failed" {
			result.Cleanup = "incomplete"
		}
		result.Problems = append(result.Problems, scenarioruntime.Problem{Phase: "reporting", Code: "incomplete_evidence", Message: "Not all planned tests produced reports"})
	}
	if retained {
		result.Cleanup = "not_run"
	}
	return result
}
