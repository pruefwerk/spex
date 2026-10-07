package spex

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pruefwerk/spex/internal/workspace"
	"github.com/pruefwerk/spex/pkg/definition"
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
	flags := r.flags
	flags, err = overlay.ResolveControls(flags, request.Workspace)
	if err != nil {
		return nil, err
	}
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
	inline := map[string][]byte{}
	logicalSources := map[string]string{}
	if len(request.Scenario.Tests) > 0 {
		existingRefs := resolved.ScenarioRefs
		resolved.ScenarioRefs = nil
		resolved.ScenarioPaths = nil
		for index, source := range request.Scenario.Tests {
			var sourcePath string
			if source.File != nil {
				sourcePath, err = scenario.SourcePath(request.Workspace, *source.File)
				if err != nil {
					return nil, &scenarioSourceError{index: index}
				}
				logicalSources[sourcePath] = *source.File
			} else {
				content := []byte(*source.Inline)
				digest := sha256.Sum256(content)
				sourcePath = filepath.Join(request.Workspace, fmt.Sprintf("inline-%x-%d%s", digest[:12], index, inlineSourceExtension(*source.Inline)))
				inline[sourcePath] = content
				logicalSources[sourcePath] = fmt.Sprintf("scenario.toml:test[%d]", index)
			}
			matched := false
			if source.File != nil {
				for _, existing := range existingRefs {
					if filepath.Clean(existing.Path) == filepath.Clean(sourcePath) {
						resolved.ScenarioRefs = append(resolved.ScenarioRefs, existing)
						matched = true
					}
				}
			}
			if !matched {
				resolved.ScenarioRefs = append(resolved.ScenarioRefs, workspace.ResolvedScenarioRef{Path: sourcePath, BindingPath: resolved.BindingPath, IntegrationProfilePath: resolved.IntegrationProfilePath})
			}
		}
	}
	inputs, err := loadSuiteSourceInputs(resolved, flags, inline)
	if err != nil {
		for _, ref := range resolved.ScenarioRefs {
			if len(request.Scenario.Tests) > 0 && strings.HasPrefix(err.Error(), ref.Path+":") {
				for index, source := range request.Scenario.Tests {
					if (source.File != nil && logicalSources[ref.Path] == *source.File) || logicalSources[ref.Path] == fmt.Sprintf("scenario.toml:test[%d]", index) {
						return nil, &scenarioSourceError{index: index}
					}
				}
			}
		}
		return nil, errors.New("could not resolve runtime inputs")
	}
	resolved, inputs, flags, err = overlay.Apply(resolved, inputs, flags)
	if err != nil {
		return nil, err
	}
	return &preparedMigrationRuntime{resolved: resolved, inputs: inputs, flags: flags, overridePresent: !request.Scenario.RuntimeConfig.Empty(), logicalSources: logicalSources}, nil
}

type scenarioSourceError struct{ index int }

func (e *scenarioSourceError) Error() string {
	return fmt.Sprintf("scenario.toml:test[%d]: invalid Spex source or binding", e.index)
}

func inlineSourceExtension(source string) string {
	return definition.Extension(source)
}

type preparedMigrationRuntime struct {
	resolved        workspace.ResolvedScenarioSuite
	inputs          []workspace.Inputs
	flags           suiteFlags
	overridePresent bool
	logicalSources  map[string]string
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
		source := r.logicalSources[input.ScenarioPath]
		if source == "" {
			source = filepath.Base(input.ScenarioPath)
		}
		summary.Tests = append(summary.Tests, scenarioruntime.TestDescription{Name: input.ScenarioName, Source: source})
	}
	return migrationExecutionPlan{owner: r, summary: summary}, nil
}

func (r *preparedMigrationRuntime) RedactedDescription() any {
	return struct {
		Runtime string `json:"runtime"`
		Version string `json:"version"`
	}{"migration-testbench/v1", Version}
}

func (r *preparedMigrationRuntime) Execute(ctx context.Context, plan scenarioruntime.ExecutionPlan, sink scenarioruntime.ArtifactSink) (result scenarioruntime.ExecutionResult, executeErr error) {
	result = scenarioruntime.ExecutionResult{Outcome: scenarioruntime.Error, Cleanup: "not_run"}
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
	private, err := os.MkdirTemp("", "spex-scenario-")
	if err != nil {
		return result, errors.New("cannot create private execution workspace")
	}
	defer func() {
		if err := os.RemoveAll(private); err != nil {
			result.Problems = append(result.Problems, scenarioruntime.Problem{Phase: "cleanup", Code: "workspace_cleanup_failed", Message: "Private execution workspace cleanup failed"})
			if result.Outcome == scenarioruntime.Passed {
				result.Outcome = scenarioruntime.Error
			}
			if executeErr == nil {
				executeErr = errors.New("private workspace cleanup failed")
			}
		}
	}()
	flags.outPath = filepath.Join(private, "workspaces")
	err = runResolvedSuite(r.resolved, r.inputs, flags, io.Discard, io.Discard)
	result = migrationExecutionEvidence(flags.outPath, len(p.summary.Tests), flags.retainRuntime)
	allowedNames := map[string]bool{}
	for _, t := range p.summary.Tests {
		allowedNames[t.Name] = true
	}
	for i := range result.Tests {
		if !allowedNames[result.Tests[i].Name] {
			result.Tests[i].Name = fmt.Sprintf("test-%d", i)
		}
	}
	if evidenceErr := persistMigrationEvidence(flags.outPath, sink); evidenceErr != nil {
		result.Problems = append(result.Problems, scenarioruntime.Problem{Phase: "reporting", Code: "evidence_write_failed", Message: "Safe test evidence could not be persisted"})
		if result.Outcome == scenarioruntime.Passed {
			result.Outcome = scenarioruntime.Error
		}
		if err == nil {
			err = evidenceErr
		}
	}
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

// Do not copy raw logs, generated manifests, kubeconfig or resolved bindings.
// Backend responses and setup output can contain credentials unknown to Spex.
// Export a typed projection instead of guessing which strings need redaction.
func persistMigrationEvidence(root string, sink scenarioruntime.ArtifactSink) error {
	type step struct {
		Ordinal  int    `json:"ordinal"`
		Internal bool   `json:"internal"`
		Outcome  string `json:"outcome"`
	}
	type test struct {
		Index   int    `json:"index"`
		Outcome string `json:"outcome"`
		Steps   []step `json:"steps"`
	}
	evidence := struct {
		Schema string `json:"schema"`
		Tests  []test `json:"tests"`
	}{Schema: "spex.migration-evidence/v1", Tests: []test{}}
	paths, err := filepath.Glob(filepath.Join(root, "*", "reports", "scenario-run-report.json"))
	if err != nil {
		return errors.New("cannot collect evidence")
	}
	for index, path := range paths {
		data, err := readRegularEvidenceFile(path, maxScenarioReportSummarySize)
		if err != nil {
			return errors.New("cannot read test evidence")
		}
		var report ScenarioRunReport
		if json.Unmarshal(data, &report) != nil {
			return errors.New("invalid test evidence")
		}
		item := test{Index: index, Outcome: safeEvidenceOutcome(report.Status.Result), Steps: []step{}}
		for ordinal, s := range report.Steps {
			item.Steps = append(item.Steps, step{Ordinal: ordinal, Internal: s.Internal, Outcome: safeEvidenceOutcome(s.Result)})
		}
		evidence.Tests = append(evidence.Tests, item)
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return errors.New("cannot encode test evidence")
	}
	return sink.Write("artifacts/test-evidence.json", data)
}

func safeEvidenceOutcome(value string) string {
	switch value {
	case "passed", "failed", "error", "not_run", "skipped", "cancelled":
		return value
	}
	return "unknown"
}

func migrationExecutionEvidence(root string, expected int, retained bool) scenarioruntime.ExecutionResult {
	result := scenarioruntime.ExecutionResult{Outcome: scenarioruntime.Passed, Cleanup: "succeeded", Artifacts: []string{"artifacts/test-evidence.json"}}
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
		if report.Status.FailureClass != nil && *report.Status.FailureClass == "cancelled" {
			outcome = scenarioruntime.Cancelled
		}
		if report.Status.CleanupFailed || (report.Status.FailureClass != nil && *report.Status.FailureClass == "runtime_cleanup_failed") {
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
