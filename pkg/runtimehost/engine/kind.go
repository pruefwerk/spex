package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
	"os"

	"github.com/pruefwerk/spex/pkg/migrationtestbench"
	"github.com/pruefwerk/spex/pkg/receiver"
	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

type lifecycle func(context.Context, string) error
type kindRuntime struct {
	host *Host
	receiver.Implementation
	lifecycle lifecycle
	root      string
	scope     hostconfig.Scope
}
type kindPrepared struct {
	host *Host
	scenarioruntime.PreparedRuntime
	lifecycle  lifecycle
	id         string
	groupGuard func(context.Context) error
	scope      hostconfig.Scope
}
type kindPlan struct {
	scenarioruntime.ExecutionPlan
	owner *kindPrepared
}

func (h *Host) newKindRuntime(admitted admissionContext, release string) (receiver.Implementation, error) {
	root := admitted.Root
	if _, err := h.checkExecution(root, executionPaths{admitted.Scope, admitted.Suite, admitted.Kubeconfig}, nil); err != nil {
		return nil, err
	}
	base, err := migrationtestbench.New(migrationtestbench.Config{RepositoryRoot: root, SuitePath: admitted.Suite, Release: release})
	if err != nil {
		return nil, err
	}
	command := func(ctx context.Context, operation string) error {
		return h.kindOperation(ctx, root, admitted.Scope, operation)
	}
	scope, err := h.resolver.LoadScope(admitted.Scope)
	if err != nil {
		return nil, err
	}
	return &kindRuntime{host: h, Implementation: base, lifecycle: command, root: root, scope: scope}, nil
}

// Use the same owned-resource command for receiver cleanup and capacity release.
// The ledger implementation verifies exact node IDs before deleting resources.
func (h *Host) kindOperation(ctx context.Context, root, scope, operation string) error {
	if operation != "create" && operation != "cleanup" {
		return errors.New("invalid Kind lifecycle operation")
	}
	cluster, err := h.nativeKind(root, scope, nil)
	if err != nil {
		return err
	}
	if operation == "create" {
		return cluster.Create(ctx)
	}
	return cluster.Cleanup(ctx)
}

func (r *kindRuntime) Resolve(ctx context.Context, request scenarioruntime.ResolveRequest) (scenarioruntime.PreparedRuntime, error) {
	if len(request.Scenario.Tests) != 0 || len(request.Scenario.Dependencies) != 0 {
		return nil, errors.New("receiver currently supports trusted runtime discovery only")
	}
	id, err := scenario.Identity(request.Scenario)
	if err != nil {
		return nil, err
	}
	// With no caller file sources admitted, runtime-owned profiles and hooks use
	// the trusted checkout. The canonical scenario remains the caller's overlay;
	// these host defaults are inherited execution r.host.configuration, not authoring.
	defaultConfig := ""
	if os.Getenv("GITHUB_ACTIONS") == "true" && r.host.config.BeforeScenarioHook != "" {
		policy := r.host.config
		defaultConfig = fmt.Sprintf("[execution]\nbefore_scenario_hook = %q\nbefore_scenario_hook_timeout = %q\n", policy.BeforeScenarioHook, policy.BeforeScenarioHookTimeout)
	}
	defaults, err := scenario.ParseRuntimeConfig([]byte(defaultConfig))
	if err != nil {
		return nil, err
	}
	request.Scenario.RuntimeConfig, err = r.Implementation.MergeConfig(defaults, request.Scenario.RuntimeConfig)
	if err != nil {
		return nil, err
	}
	request.Workspace = r.root
	base, err := r.Implementation.Resolve(ctx, request)
	if err != nil {
		return nil, err
	}
	return &kindPrepared{host: r.host, PreparedRuntime: base, lifecycle: r.lifecycle, id: id, scope: r.scope}, nil
}

func (r *kindPrepared) Plan(ctx context.Context) (scenarioruntime.ExecutionPlan, error) {
	plan, err := r.PreparedRuntime.Plan(ctx)
	if err != nil {
		return nil, err
	}
	if plan == nil || len(plan.Summary().Tests) == 0 || len(plan.Summary().ResourceClaims) != 0 {
		return nil, errors.New("private Kind plan invalid")
	}
	return kindPlan{plan, r}, nil
}

func (r *kindPrepared) Execute(ctx context.Context, plan scenarioruntime.ExecutionPlan, sink scenarioruntime.ArtifactSink) (result scenarioruntime.ExecutionResult, primary error) {
	result = scenarioruntime.ExecutionResult{Outcome: scenarioruntime.Error, Cleanup: "not_run"}
	p, ok := plan.(kindPlan)
	if !ok || p.owner != r || sink == nil {
		return result, errors.New("invalid Kind execution plan")
	}
	// Plan ownership already proves a private Kind stack. Keep its contract in
	// the same typed resolver used for shared-stack policy, without inventing
	// claims on another execution's private broker.
	contract, err := r.host.resolver.ResourceContract(r.scope, r.id, p.Summary().Tests, nil)
	if err != nil {
		return result, err
	}
	data, _ := json.Marshal(contract)
	if sink.Write("resource-contract.json", data) != nil {
		return result, errors.New("cannot persist Kind resource contract")
	}
	if r.groupGuard != nil {
		// Split CI groups borrow a host-admitted cluster. They neither create it
		// nor release its capacity; only the final host cleanup may do that.
		if err := r.groupGuard(ctx); err != nil {
			return result, errors.New("group execution ownership or capacity unconfirmed")
		}
		data, _ := json.Marshal(struct {
			Cleanup  string `json:"cleanup"`
			Capacity string `json:"capacity"`
		}{"deferred_to_host", "held_by_host"})
		if sink.Write("cluster-lifecycle.json", data) != nil {
			return result, errors.New("cannot persist cluster lifetime evidence")
		}
		result, primary = r.PreparedRuntime.Execute(ctx, p.ExecutionPlan, sink)
		if result.Cleanup == "succeeded" || result.Cleanup == "not_run" {
			// Use the existing result vocabulary. The separate lifecycle artifact
			// explains why cluster cleanup has not r.host.run; preserve probe cleanup errors.
			result.Cleanup = "not_run"
		}
		result.Artifacts = append(result.Artifacts, "resource-contract.json", "cluster-lifecycle.json")
		return result, primary
	}
	lifecycle := scenarioruntime.Lifecycle{
		Setup:          func(ctx context.Context) error { return r.lifecycle(ctx, "create") },
		Cleanup:        func(ctx context.Context) error { return r.lifecycle(ctx, "cleanup") },
		CleanupTimeout: hostconfig.Deadline(r.host.config.CleanupTimeout),
		SetupProblem:   scenarioruntime.Problem{Phase: "environment", Code: "kind_setup_failed", Message: "Kind preparation did not complete"},
		CleanupProblem: scenarioruntime.Problem{Phase: "cleanup", Code: "kind_cleanup_incomplete", Message: "Execution-owned Kind cleanup requires ledger review"},
	}
	result, primary = lifecycle.Execute(ctx, func(ctx context.Context) (scenarioruntime.ExecutionResult, error) {
		return r.PreparedRuntime.Execute(ctx, p.ExecutionPlan, sink)
	})
	result.Artifacts = append(result.Artifacts, "resource-contract.json")
	return result, primary
}
