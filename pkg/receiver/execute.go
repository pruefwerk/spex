package receiver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/pruefwerk/spex/pkg/definition"
	"github.com/pruefwerk/spex/pkg/resourceclaims"
	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

// Checkout is attested by the host's authenticated source fetcher. Keep this
// read-only source tree separate from trusted runtime configuration and outputs.
type Checkout struct{ Directory, Repository, Commit string }

type Implementation interface {
	scenarioruntime.Runtime
	scenarioruntime.ConfigAuthor
	Release() string
}
type Choice struct {
	Runtime Implementation
	Default bool
}

// Policy has no permissive default. Admit checks the authenticated source/actor
// policy; Claim must durably reject duplicate request IDs; Allow checks resolved
// tests and configuration before runtime resolution or environment preparation.
type Policy interface {
	Admit(context.Context, Envelope) error
	Claim(context.Context, string) error
	Allow(context.Context, scenario.Scenario) error
}

type Execution struct {
	WorkflowSHA string
	RunID       int64
	RunAttempt  int
	SpexVersion string
}
type Host struct {
	Policy              Policy
	Runtimes            []Choice
	DefaultRuntime      string
	Execution           Execution
	ResourceCoordinator *resourceclaims.Coordinator
	Scheduling          *Scheduling
}

type Result struct {
	scenarioruntime.ExecutionResult
	SpexVersion string `json:"spex_version,omitempty"`
}
type Receipt struct {
	Schema        string `json:"schema"`
	RequestID     string `json:"request_id"`
	RequestSHA256 string `json:"request_sha256"`
	SourceCommit  string `json:"source_commit"`
	WorkflowSHA   string `json:"workflow_sha"`
	RunID         int64  `json:"run_id"`
	RunAttempt    int    `json:"run_attempt"`
	PlannedTests  int    `json:"planned_tests"`
	Result        Result `json:"result"`
}

// Execute has one execution implementation beneath it: scenarioruntime.Run.
// It never fetches source, handles credentials, starts a workflow or chooses a
// physical artifact directory. All of those remain host responsibilities.
func Execute(ctx context.Context, request Request, checkout Checkout, host Host, sink scenarioruntime.ArtifactSink) (receipt Receipt, primary error) {
	x := host.Execution
	if !digestPattern.MatchString(request.digest) || !shaPattern.MatchString(x.WorkflowSHA) || x.RunID <= 0 || x.RunAttempt != 1 || !versionPattern.MatchString(x.SpexVersion) || x.SpexVersion == "latest" || sink == nil {
		return receipt, errors.New("invalid receiver execution identity or artifact sink")
	}
	e := request.envelope
	receipt = Receipt{Schema: "spex.transport-result/v1", RequestID: e.RequestID, RequestSHA256: request.digest, SourceCommit: e.Source.Commit, WorkflowSHA: x.WorkflowSHA, RunID: x.RunID, RunAttempt: x.RunAttempt, Result: Result{ExecutionResult: scenarioruntime.ExecutionResult{Schema: "spex.result/v1", Outcome: scenarioruntime.Error, Cleanup: "not_run", Tests: []scenarioruntime.TestResult{}, Artifacts: []string{}}}}
	defer func() {
		if ctx.Err() != nil && receipt.Result.Outcome == scenarioruntime.Error && receipt.Result.ScenarioID == "" {
			receipt.Result.Outcome = scenarioruntime.Cancelled
		}
		data, err := json.Marshal(receipt)
		if err == nil && len(data) > MaxReceiptBytes {
			err = errors.New("receipt exceeds size limit")
		}
		if err == nil {
			err = sink.Write("receipt.json", data)
		}
		if err != nil {
			if receipt.Result.Outcome == scenarioruntime.Passed {
				receipt.Result.Outcome = scenarioruntime.Error
			}
			if primary == nil {
				primary = errors.New("receiver could not persist receipt")
			}
		}
	}()
	fail := func(phase string) (Receipt, error) { return receipt, errors.New("receiver " + phase + " failed") }
	if ctx.Err() != nil {
		return receipt, ctx.Err()
	}
	if host.Policy == nil {
		return fail("admission policy")
	}
	if err := host.Policy.Admit(ctx, request.Envelope()); err != nil {
		return fail("admission")
	}
	if ctx.Err() != nil {
		return receipt, ctx.Err()
	}
	if err := host.Policy.Claim(ctx, e.RequestID); err != nil {
		return fail("request claim")
	}
	if checkout.Repository != e.Source.Repository || checkout.Commit != e.Source.Commit {
		return fail("source identity")
	}
	root, err := filepath.EvalSymlinks(checkout.Directory)
	if err != nil {
		return fail("source checkout")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return fail("source checkout")
	}
	if e.Source.Root != "." {
		root, err = scenario.SourcePath(root, e.Source.Root)
		if err != nil {
			return fail("source base")
		}
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return fail("source base")
	}
	var inline *string
	if e.Authoring.Definition != "" {
		inline = &e.Authoring.Definition
	}
	document, tests, err := definition.Load(inline, e.Authoring.DefinitionFiles, root, readSource, true)
	if err != nil {
		return fail("definition parsing")
	}
	base := scenario.Scenario{}
	if document != nil {
		base = *document
	}
	if !base.RuntimeOverlay.Empty() {
		return fail("obsolete runtime overlay")
	}
	overrides := scenario.AuthoringOverrides{Tests: tests}
	if e.Authoring.Runtime != "" {
		overrides.Runtime = &e.Authoring.Runtime
	}
	if e.Authoring.Name != "" {
		overrides.Name = &e.Authoring.Name
	}
	if e.Authoring.Description != "" {
		overrides.Description = &e.Authoring.Description
	}
	if e.Authoring.Timeout != "" {
		overrides.Timeout = &e.Authoring.Timeout
	}
	draft, err := scenario.AuthorRequest(base, overrides, document != nil)
	if err != nil {
		return fail("definition authoring")
	}
	catalog := make([]scenario.RuntimeRelease, 0, len(host.Runtimes))
	for _, choice := range host.Runtimes {
		if choice.Runtime == nil {
			return fail("runtime catalog")
		}
		catalog = append(catalog, scenario.RuntimeRelease{Contract: choice.Runtime.ID(), Release: choice.Runtime.Release(), Default: choice.Default, Merge: choice.Runtime.MergeConfig})
	}
	resolved, err := scenario.ResolveRequest(draft, catalog, host.DefaultRuntime)
	if err != nil {
		return fail("runtime selection")
	}
	if err := scenario.ValidateSources(resolved, root); err != nil {
		return fail("source dependencies")
	}
	if len(resolved.Dependencies) > 1000 {
		return fail("dependency limit")
	}
	for _, dependency := range resolved.Dependencies {
		file, err := scenario.SourcePath(root, dependency)
		if err != nil {
			return fail("dependency path")
		}
		info, err := os.Stat(file)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
			return fail("dependency file")
		}
	}
	if ctx.Err() != nil {
		return receipt, ctx.Err()
	}
	// Give policy a canonical copy, not mutable runtime-owned slices.
	policyView, err := scenario.Canonicalize(resolved)
	if err != nil {
		return fail("canonicalization")
	}
	if err := host.Policy.Allow(ctx, policyView); err != nil {
		return fail("scenario admission")
	}
	registry := scenarioruntime.NewRegistry(scenarioruntime.WithResourceClaims(host.ResourceCoordinator))
	for _, choice := range host.Runtimes {
		if choice.Runtime.ID() == resolved.Runtime && choice.Runtime.Release() == resolved.RuntimeRelease {
			if err := registry.Register(choice.Runtime); err != nil {
				return fail("runtime registration")
			}
		}
	}
	prepared, err := registry.Prepare(ctx, scenarioruntime.ResolveRequest{Scenario: resolved, Workspace: root})
	if err != nil {
		return fail("planning")
	}
	receipt.PlannedTests = len(prepared.Plan.Summary().Tests)
	result, runErr := RunScheduled(ctx, prepared, sink, e.RequestID, host.Scheduling)
	if result.Schema == "" {
		if runErr == nil {
			runErr = errors.New("receiver execution returned no result")
		}
		return receipt, runErr
	}
	receipt.Result = Result{ExecutionResult: result, SpexVersion: x.SpexVersion}
	if result.Outcome == scenarioruntime.Passed {
		complete := receipt.PlannedTests > 0 && len(result.Tests) == receipt.PlannedTests && len(result.Problems) == 0 && (result.Cleanup == "succeeded" || result.Cleanup == "not_run")
		for _, test := range result.Tests {
			complete = complete && test.Outcome == scenarioruntime.Passed
		}
		if !complete {
			receipt.Result.Outcome = scenarioruntime.Error
			if runErr == nil {
				runErr = errors.New("receiver execution did not establish complete success")
			}
		}
	}
	return receipt, runErr
}

// ExitCode preserves primary test failure/cancellation if receipt persistence
// also failed. Workflow hosts must propagate this status after artifact upload.
func ExitCode(receipt Receipt, err error) int {
	if receipt.Result.Outcome == scenarioruntime.Failed {
		return 3
	}
	if receipt.Result.Outcome == scenarioruntime.Cancelled {
		return 130
	}
	if err == nil && receipt.Result.Outcome == scenarioruntime.Passed {
		return 0
	}
	return 1
}

func readSource(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("source unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > scenario.MaxDocumentBytes {
		return nil, errors.New("invalid source file")
	}
	data, err := io.ReadAll(io.LimitReader(file, scenario.MaxDocumentBytes+1))
	if err != nil || len(data) > scenario.MaxDocumentBytes {
		return nil, errors.New("cannot read source")
	}
	return data, nil
}
