package spex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

func TestScenarioInlineAndFileUseSameEngine(t *testing.T) {
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()
	flags, err := parseSuiteFlags("run", []string{"--suite", "examples/suites/mqtt-local.yaml", "--run-id", "sources", "--command", writeFakeKubectl(t, 0, "")})
	if err != nil {
		t.Fatal(err)
	}
	registry := scenarioruntime.NewRegistry()
	if err := registry.Register(migrationRuntime{flags: flags}); err != nil {
		t.Fatal(err)
	}
	file := "examples/scenarios/mqtt-ingestion-basic.yaml"
	content, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		t.Fatal(err)
	}
	inline := string(content)
	var inputs []*preparedMigrationRuntime
	for _, source := range []scenario.TestSource{{File: &file}, {Inline: &inline}} {
		prepared, err := registry.Prepare(context.Background(), scenarioruntime.ResolveRequest{Workspace: root, Scenario: scenario.Scenario{Schema: scenario.Schema, Runtime: "migration-testbench/v1", Tests: []scenario.TestSource{source}}})
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, prepared.Runtime.(*preparedMigrationRuntime))
		result, err := prepared.Execute(context.Background(), runtimeTestSink(t.TempDir()))
		if err != nil || result.Outcome != scenarioruntime.Passed || len(result.Tests) != 1 {
			t.Fatalf("source execution: %+v, %v", result, err)
		}
	}
	if !reflect.DeepEqual(inputs[0].inputs[0].Scenario, inputs[1].inputs[0].Scenario) || !reflect.DeepEqual(inputs[0].inputs[0].Binding, inputs[1].inputs[0].Binding) {
		t.Fatal("inline changed test semantics")
	}
	if _, err := os.Stat(inputs[1].inputs[0].ScenarioPath); !os.IsNotExist(err) {
		t.Fatal("inline preparation materialized a source file")
	}
}

func TestScenarioInlineErrorHasStableLocation(t *testing.T) {
	root := repoRoot(t)
	flags, err := parseSuiteFlags("run", []string{"--suite", filepath.Join(root, "examples/suites/mqtt-local.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	registry := scenarioruntime.NewRegistry()
	_ = registry.Register(migrationRuntime{flags: flags})
	inline := "invalid: [SENTINEL_SECRET"
	_, err = registry.Prepare(context.Background(), scenarioruntime.ResolveRequest{Workspace: root, Scenario: scenario.Scenario{Schema: scenario.Schema, Runtime: "migration-testbench/v1", Tests: []scenario.TestSource{{Inline: &inline}}}})
	var sourceError *scenarioSourceError
	if !errors.As(err, &sourceError) || sourceError.Error() != "scenario.toml:test[0]: invalid Spex source or binding" {
		t.Fatalf("unstable source error: %v", err)
	}
}

func TestRepeatedInlineSourcesKeepDistinctLocations(t *testing.T) {
	root := repoRoot(t)
	flags, err := parseSuiteFlags("run", []string{"--suite", filepath.Join(root, "examples/suites/mqtt-local.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	registry := scenarioruntime.NewRegistry()
	_ = registry.Register(migrationRuntime{flags: flags})
	inline := scenarioSmokeSource
	prepared, err := registry.Prepare(context.Background(), scenarioruntime.ResolveRequest{Workspace: root, Scenario: scenario.Scenario{Schema: scenario.Schema, Runtime: "migration-testbench/v1", Tests: []scenario.TestSource{{Inline: &inline}, {Inline: &inline}}}})
	if err != nil {
		t.Fatal(err)
	}
	tests := prepared.Plan.Summary().Tests
	if len(tests) != 2 || tests[0].Source != "scenario.toml:test[0]" || tests[1].Source != "scenario.toml:test[1]" {
		t.Fatalf("source locations collided: %+v", tests)
	}
}
