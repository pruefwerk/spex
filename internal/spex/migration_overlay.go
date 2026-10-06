package spex

import (
	"errors"

	"github.com/pelletier/go-toml/v2"
	"github.com/pruefwerk/spex/internal/workspace"
	"github.com/pruefwerk/spex/pkg/scenario"
)

// Optional pointers distinguish inheritance from an explicit zero or false.
// The first overlay surface intentionally excludes credentials and service
// topology. Those remain in the repository's existing configuration.
type migrationOverlay struct {
	Suite       *string                     `toml:"suite,omitempty"`
	Environment migrationEnvironmentOverlay `toml:"environment,omitempty"`
	Probe       migrationProbeOverlay       `toml:"probe,omitempty"`
	Execution   migrationExecutionOverlay   `toml:"execution,omitempty"`
}

type migrationEnvironmentOverlay struct {
	Namespace   *string `toml:"namespace,omitempty"`
	KubeContext *string `toml:"kube_context,omitempty"`
}

type migrationProbeOverlay struct {
	Image           *string `toml:"image,omitempty"`
	ImagePullPolicy *string `toml:"image_pull_policy,omitempty"`
}

type migrationExecutionOverlay struct {
	FailFast               *bool `toml:"fail_fast,omitempty"`
	Repetitions            *int  `toml:"repetitions,omitempty"`
	Concurrency            *int  `toml:"concurrency,omitempty"`
	MaxFailures            *int  `toml:"max_failures,omitempty"`
	RetainRuntimeResources *bool `toml:"retain_runtime_resources,omitempty"`
	CollectResourceUsage   *bool `toml:"collect_resource_usage,omitempty"`
}

func decodeMigrationOverlay(raw scenario.RawRuntimeConfig) (migrationOverlay, error) {
	var overlay migrationOverlay
	if err := raw.DecodeStrict(&overlay); err != nil {
		return overlay, err
	}
	if overlay.Suite != nil {
		if _, err := scenario.RelativePath(*overlay.Suite); err != nil {
			return overlay, errors.New("runtime suite must be workspace-relative")
		}
	}
	if overlay.Environment.Namespace != nil && *overlay.Environment.Namespace == "" {
		return overlay, errors.New("runtime namespace must not be empty")
	}
	if overlay.Probe.Image != nil && *overlay.Probe.Image == "" {
		return overlay, errors.New("runtime probe image must not be empty")
	}
	for _, count := range []*int{overlay.Execution.Repetitions, overlay.Execution.Concurrency, overlay.Execution.MaxFailures} {
		if count != nil && *count < 0 {
			return overlay, errors.New("runtime execution counts must not be negative")
		}
	}
	return overlay, nil
}

func overlayValue[T any](base, override *T) *T {
	if override != nil {
		copy := *override
		return &copy
	}
	if base != nil {
		copy := *base
		return &copy
	}
	return nil
}

func (migrationRuntime) MergeConfig(baseRaw, overrideRaw scenario.RawRuntimeConfig) (scenario.RawRuntimeConfig, error) {
	base, err := decodeMigrationOverlay(baseRaw)
	if err != nil {
		return scenario.RawRuntimeConfig{}, err
	}
	override, err := decodeMigrationOverlay(overrideRaw)
	if err != nil {
		return scenario.RawRuntimeConfig{}, err
	}
	base.Suite = overlayValue(base.Suite, override.Suite)
	base.Environment.Namespace = overlayValue(base.Environment.Namespace, override.Environment.Namespace)
	base.Environment.KubeContext = overlayValue(base.Environment.KubeContext, override.Environment.KubeContext)
	base.Probe.Image = overlayValue(base.Probe.Image, override.Probe.Image)
	base.Probe.ImagePullPolicy = overlayValue(base.Probe.ImagePullPolicy, override.Probe.ImagePullPolicy)
	base.Execution.FailFast = overlayValue(base.Execution.FailFast, override.Execution.FailFast)
	base.Execution.Repetitions = overlayValue(base.Execution.Repetitions, override.Execution.Repetitions)
	base.Execution.Concurrency = overlayValue(base.Execution.Concurrency, override.Execution.Concurrency)
	base.Execution.MaxFailures = overlayValue(base.Execution.MaxFailures, override.Execution.MaxFailures)
	base.Execution.RetainRuntimeResources = overlayValue(base.Execution.RetainRuntimeResources, override.Execution.RetainRuntimeResources)
	base.Execution.CollectResourceUsage = overlayValue(base.Execution.CollectResourceUsage, override.Execution.CollectResourceUsage)
	data, err := toml.Marshal(base)
	if err != nil {
		return scenario.RawRuntimeConfig{}, errors.New("cannot encode runtime overlay")
	}
	return scenario.ParseRuntimeConfig(data)
}

// Apply copies the values it changes. It neither reloads configuration nor
// mutates the caller's resolved configuration or overlay pointers.
func (o migrationOverlay) Apply(base workspace.ResolvedScenarioSuite, inputs []workspace.Inputs, flags suiteFlags) (workspace.ResolvedScenarioSuite, []workspace.Inputs, suiteFlags, error) {
	result := base
	if o.Execution.FailFast != nil {
		flags.failFast = false
		result.Suite.Spec.Execution.FailFast = overlayValue[bool](nil, o.Execution.FailFast)
	}
	if o.Execution.Repetitions != nil {
		result.Suite.Spec.Execution.Repetitions = *o.Execution.Repetitions
	}
	if o.Execution.Concurrency != nil {
		result.Suite.Spec.Execution.Concurrency = *o.Execution.Concurrency
	}
	if o.Execution.MaxFailures != nil {
		result.Suite.Spec.Execution.MaxFailures = *o.Execution.MaxFailures
	}
	if o.Execution.RetainRuntimeResources != nil {
		flags.retainRuntime = *o.Execution.RetainRuntimeResources
	}
	if o.Execution.CollectResourceUsage != nil {
		flags.collectResources = *o.Execution.CollectResourceUsage
	}
	out := append([]workspace.Inputs(nil), inputs...)
	for i := range out {
		if o.Environment.Namespace != nil {
			out[i].Namespace = *o.Environment.Namespace
			out[i].Binding.Spec.Namespace = *o.Environment.Namespace
		}
		if o.Environment.KubeContext != nil {
			out[i].KubeContext = *o.Environment.KubeContext
			out[i].Binding.Spec.KubeContext = *o.Environment.KubeContext
		}
		if o.Probe.Image != nil {
			out[i].Binding.Spec.Probe.Image = *o.Probe.Image
		}
		if o.Probe.ImagePullPolicy != nil {
			out[i].Binding.Spec.Probe.ImagePullPolicy = *o.Probe.ImagePullPolicy
		}
		if err := workspace.ValidateRuntimeInputs(out[i]); err != nil {
			return base, inputs, flags, errors.New("invalid resolved runtime configuration")
		}
	}
	return result, out, flags, nil
}
