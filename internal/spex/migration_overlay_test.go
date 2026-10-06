package spex

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/internal/workspace"
	"github.com/pruefwerk/spex/pkg/scenario"
)

func overlayTOML(t *testing.T, value string) scenario.RawRuntimeConfig {
	t.Helper()
	raw, err := scenario.ParseRuntimeConfig([]byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMigrationOverlayTypedMerge(t *testing.T) {
	base := overlayTOML(t, `suite = "suite.yaml"
[environment]
namespace = "inherited"
kube_context = "inherited"
[execution]
fail_fast = true
repetitions = 3
max_failures = 2
retain_runtime_resources = true
`)
	override := overlayTOML(t, `[environment]
namespace = "candidate"
kube_context = ""
[execution]
fail_fast = false
max_failures = 0
retain_runtime_resources = false
`)
	merged, err := (migrationRuntime{}).MergeConfig(base, override)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeMigrationOverlay(merged)
	if err != nil {
		t.Fatal(err)
	}
	if *got.Suite != "suite.yaml" || *got.Environment.Namespace != "candidate" || *got.Environment.KubeContext != "" || *got.Execution.FailFast || *got.Execution.MaxFailures != 0 || *got.Execution.Repetitions != 3 || *got.Execution.RetainRuntimeResources {
		t.Fatalf("wrong typed merge: %s", merged.Bytes())
	}
	original, _ := decodeMigrationOverlay(base)
	if *original.Environment.Namespace != "inherited" || !*original.Execution.FailFast {
		t.Fatal("merge mutated its input")
	}
}

func TestMigrationOverlayRejectsInvalidFieldsAndValues(t *testing.T) {
	for _, input := range []string{
		`profiel = "migration"`, `suite = "../outside.yaml"`,
		`[execution]
repetitions = -1`, `[environment]
namespace = ""`, `[probe]
image = ""`, `[execution]
fail_fast = "false"`, `password = "SENTINEL_PRIVATE_VALUE"`,
	} {
		_, err := decodeMigrationOverlay(overlayTOML(t, input))
		if err == nil {
			t.Fatalf("accepted invalid overlay: %s", input)
		}
		if strings.Contains(err.Error(), "SENTINEL_PRIVATE_VALUE") {
			t.Fatal("error exposed runtime input")
		}
	}
}

func TestMigrationOverlayApplicationIsPure(t *testing.T) {
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()
	flags, err := parseSuiteFlags("run", []string{"--suite", filepath.Join(root, "examples/suites/mqtt-local.yaml"), "--run-id", "overlay", "--fail-fast", "--retain-runtime-resources"})
	if err != nil {
		t.Fatal(err)
	}
	base, err := workspace.LoadScenarioSuite(flags.suitePath)
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := loadSuiteInputs(base, flags)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := decodeMigrationOverlay(scenario.RawRuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	same, sameInputs, sameFlags, err := empty.Apply(base, inputs, flags)
	if err != nil || !reflect.DeepEqual(base, same) || !reflect.DeepEqual(inputs, sameInputs) || !reflect.DeepEqual(flags, sameFlags) {
		t.Fatalf("empty overlay changed inherited config: %v", err)
	}
	originalNamespace := inputs[0].Namespace
	overlay, err := decodeMigrationOverlay(overlayTOML(t, `[environment]
namespace = "candidate"
kube_context = ""
[execution]
fail_fast = false
max_failures = 0
retain_runtime_resources = false
`))
	if err != nil {
		t.Fatal(err)
	}
	resolved, changed, changedFlags, err := overlay.Apply(base, inputs, flags)
	if err != nil {
		t.Fatal(err)
	}
	if changed[0].Namespace != "candidate" || changed[0].Binding.Spec.Namespace != "candidate" || changed[0].KubeContext != "" || suiteFailFast(resolved, changedFlags) || changedFlags.retainRuntime {
		t.Fatal("explicit overlay lost precedence")
	}
	if inputs[0].Namespace != originalNamespace || !flags.failFast || !flags.retainRuntime {
		t.Fatal("application mutated inherited config")
	}
	*resolved.Suite.Spec.Execution.FailFast = true
	if *overlay.Execution.FailFast {
		t.Fatal("resolved config aliases overlay storage")
	}
}
