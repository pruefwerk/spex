package engine

import (
	"context"
	"errors"
	"github.com/pelletier/go-toml/v2"
	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
	"os"
	"path/filepath"

	"github.com/pruefwerk/spex/pkg/receiver"
	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

// Suite discovery and committed scenarios converge before resolution. Neither
// input format selects a different environment or bypasses the Kind host.
func (h *Host) prepareHostInput(ctx context.Context, root string, paths executionPaths, source string) (scenarioruntime.Prepared, error) {
	return h.prepareSelectedHostInput(ctx, root, paths, source, "")
}

func (h *Host) prepareSelectedHostInput(ctx context.Context, root string, paths executionPaths, source, group string) (scenarioruntime.Prepared, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return scenarioruntime.Prepared{}, err
	}
	s := scenario.Scenario{Schema: scenario.Schema, Runtime: h.config.Runtime, RuntimeRelease: "local"}
	if source != "" && group != "" {
		return scenarioruntime.Prepared{}, errors.New("choose scenario input or CI group selection")
	}
	if group != "" {
		names, err := hostconfig.Names(group)
		if err != nil {
			return scenarioruntime.Prepared{}, err
		}
		tags := make([]string, 0, len(names))
		for _, name := range names {
			tag := "group-" + name
			if name == "baseline" {
				tag = h.config.BaselineTag
			}
			if !h.allowedGroupTags()[tag] {
				return scenarioruntime.Prepared{}, errors.New("unknown group")
			}
			tags = append(tags, tag)
		}
		var overlay struct {
			Selection struct {
				IncludeTags []string `toml:"include_any_tags"`
			} `toml:"selection"`
		}
		overlay.Selection.IncludeTags = tags
		data, err := toml.Marshal(overlay)
		if err != nil {
			return scenarioruntime.Prepared{}, err
		}
		s.RuntimeConfig, err = scenario.ParseRuntimeConfig(data)
		if err != nil {
			return scenarioruntime.Prepared{}, err
		}
	}
	if source != "" {
		path, err := scenario.SourcePath(root, source)
		if err != nil {
			return scenarioruntime.Prepared{}, errors.New("scenario source outside workspace or unavailable")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return scenarioruntime.Prepared{}, err
		}
		s, err = scenario.Parse(data)
		if err != nil {
			return scenarioruntime.Prepared{}, err
		}
		if s.RuntimeRelease == "" {
			s.RuntimeRelease = "local"
		}
	}
	// Until custom source/resource qualification lands, local and split CI runs
	// accept the same trusted discovery/selection surface as receiver execution.
	if err := (&policy{host: h, release: "local"}).Allow(ctx, s); err != nil {
		return scenarioruntime.Prepared{}, err
	}
	runtime, err := h.newKindRuntime(admissionContext{Root: root, Scope: paths.Scope, Suite: paths.Suite, Kubeconfig: paths.Kubeconfig}, "local")
	if err != nil {
		return scenarioruntime.Prepared{}, err
	}
	registry := scenarioruntime.NewRegistry()
	if err := registry.Register(runtime); err != nil {
		return scenarioruntime.Prepared{}, err
	}
	return registry.Prepare(ctx, scenarioruntime.ResolveRequest{Scenario: s, Workspace: root})
}

func (h *Host) runHostGroup(ctx context.Context, root string, paths executionPaths, source, group string) (scenarioruntime.ExecutionResult, string, error) {
	return h.runHostGroupAt(ctx, root, paths, source, group, ".spex/ci-runs")
}

func (h *Host) runHostGroupAt(ctx context.Context, root string, paths executionPaths, source, group, artifactDirectory string) (scenarioruntime.ExecutionResult, string, error) {
	prepared, err := h.prepareSelectedHostInput(ctx, root, paths, source, group)
	if err != nil {
		return scenarioruntime.ExecutionResult{}, "", err
	}
	owned, ok := prepared.Runtime.(*kindPrepared)
	if !ok {
		return scenarioruntime.ExecutionResult{}, "", errors.New("group runtime unavailable")
	}
	owned.groupGuard = func(ctx context.Context) error {
		if err := h.gateCapacity(ctx, "check", root, paths.Scope, os.Getenv, h.capacityCommand, nil); err != nil {
			return err
		}
		cluster, err := h.checkExecution(root, paths, nil)
		if err != nil {
			return err
		}
		nodes, err := cluster.OwnedNodes(ctx)
		if err != nil || len(nodes) == 0 {
			return errors.New("no live owned nodes")
		}
		return nil
	}
	id, err := scenario.Identity(prepared.Scenario)
	if err != nil {
		return scenarioruntime.ExecutionResult{}, "", err
	}
	sink, err := scenarioruntime.NewFileArtifacts(root, artifactDirectory, id)
	if err != nil {
		return scenarioruntime.ExecutionResult{}, "", err
	}
	defer sink.Close()
	result, err := scenarioruntime.Run(ctx, prepared, sink)
	return result, sink.Directory(), err
}

// Execute a one-shot plan through exactly the receiver's lifecycle and capacity
// helper. A failed plan never reaches scheduling or cluster creation.
func (h *Host) runHostInput(ctx context.Context, root string, paths executionPaths, source string) (scenarioruntime.ExecutionResult, error) {
	return h.runSelectedHostInput(ctx, root, paths, source, "")
}

func (h *Host) runSelectedHostInput(ctx context.Context, root string, paths executionPaths, source, groups string) (scenarioruntime.ExecutionResult, error) {
	// Install private paths before the existing resolver reads environment-based
	// controls, just as authenticated receiver execution does.
	for key, value := range map[string]string{h.config.ScopeEnvironment: paths.Scope, "SPEX_SUITE": paths.Suite, "KUBECONFIG": paths.Kubeconfig} {
		before, had := os.LookupEnv(key)
		if err := os.Setenv(key, value); err != nil {
			return scenarioruntime.ExecutionResult{}, err
		}
		defer func(key, before string, had bool) {
			if had {
				_ = os.Setenv(key, before)
			} else {
				_ = os.Unsetenv(key)
			}
		}(key, before, had)
	}
	prepared, err := h.prepareSelectedHostInput(ctx, root, paths, source, groups)
	if err != nil {
		return scenarioruntime.ExecutionResult{}, err
	}
	id, err := scenario.Identity(prepared.Scenario)
	if err != nil {
		return scenarioruntime.ExecutionResult{}, err
	}
	sink, err := scenarioruntime.NewFileArtifacts(root, ".spex/runs", id)
	if err != nil {
		return scenarioruntime.ExecutionResult{}, err
	}
	defer sink.Close()
	value, _, err := h.capacityIdentity(root, paths.Scope, os.Getenv)
	if err != nil {
		return scenarioruntime.ExecutionResult{}, err
	}
	admission, closeStore, err := h.openScheduling(ctx, value.Root, value.Worker)
	if err != nil {
		return scenarioruntime.ExecutionResult{}, err
	}
	defer closeStore()
	return receiver.RunScheduled(ctx, prepared, sink, value.Request, admission)
}
