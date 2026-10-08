package spex

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/internal/workspace"
	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
	"gopkg.in/yaml.v3"
)

func hostRequiredSuite(t *testing.T, root string) string {
	t.Helper()
	resolved, err := workspace.LoadScenarioSuite(filepath.Join(root, "examples/suites/mqtt-local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	suite := resolved.Suite
	suite.Spec.BindingRef = resolved.BindingPath
	suite.Spec.CatalogRefs = resolved.CatalogPaths
	suite.Spec.Scenarios = nil
	for _, source := range resolved.ScenarioPaths {
		suite.Spec.Scenarios = append(suite.Spec.Scenarios, workspace.ScenarioRef{Path: source})
	}
	suite.Spec.Execution.RequireHost = true
	data, err := yaml.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "suite.yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHostRequiredSuiteAllowsInspectionButRejectsDirectExecution(t *testing.T) {
	root := repoRoot(t)
	t.Chdir(root)
	suite := hostRequiredSuite(t, root)
	for _, command := range []string{"validate", "list", "plan"} {
		if err := Run([]string{"suite", command, "--suite", suite}, io.Discard, io.Discard); err != nil {
			t.Fatalf("read-only %s: %v", command, err)
		}
	}
	for _, command := range []string{"compile", "run"} {
		out := filepath.Join(t.TempDir(), "existing-output")
		if err := os.Mkdir(out, 0700); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(out, "keep.txt")
		if err := os.WriteFile(marker, []byte("existing evidence"), 0600); err != nil {
			t.Fatal(err)
		}
		err := Run([]string{"suite", command, "--suite", suite, "--out", out}, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "trusted runtime host") {
			t.Fatalf("%s did not reject missing host: %v", command, err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatal("admission modified existing output", err)
		}
	}
}

func TestHostRequiredSuiteRejectsStandaloneScenarioRuntime(t *testing.T) {
	root := repoRoot(t)
	t.Chdir(root)
	flags, err := parseSuiteFlags("run", []string{"--suite", hostRequiredSuite(t, root)})
	if err != nil {
		t.Fatal(err)
	}
	request := scenarioruntime.ResolveRequest{Workspace: root, Scenario: scenario.Scenario{Schema: scenario.Schema, Runtime: "migration-testbench/v1"}}
	if _, err := (migrationRuntime{flags: flags}).Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "trusted runtime host") {
		t.Fatal("standalone runtime did not require host", err)
	}
}

func TestTrustedHostCanPlanRequiredSuiteWithoutChangingRequirement(t *testing.T) {
	root := repoRoot(t)
	t.Chdir(root)
	suite := hostRequiredSuite(t, root)
	runtime, err := NewReceiverMigrationRuntime(root, suite, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := runtime.Resolve(context.Background(), scenarioruntime.ResolveRequest{Workspace: root, Scenario: scenario.Scenario{Schema: scenario.Schema, Runtime: runtime.ID()}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := prepared.Plan(context.Background())
	if err != nil || len(plan.Summary().Tests) != 5 {
		t.Fatal("trusted host changed discovery", err)
	}
	if !prepared.(*preparedMigrationRuntime).resolved.Suite.Spec.Execution.RequireHost {
		t.Fatal("host erased the suite requirement")
	}
	if _, err := decodeMigrationOverlay(overlayTOML(t, "[execution]\nrequire_host = false\n")); err == nil {
		t.Fatal("authoring overlay can override host admission")
	}
	// Use the existing simulated KUTTL runner to verify the host path without
	// deploying anything or contacting the example's application services.
	configured := runtime.(releasedMigrationRuntime)
	configured.flags.command = writeFakeKubectl(t, 0, "")
	prepared, err = configured.Resolve(context.Background(), scenarioruntime.ResolveRequest{Workspace: root, Scenario: scenario.Scenario{Schema: scenario.Schema, Runtime: runtime.ID()}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = prepared.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := prepared.Execute(context.Background(), plan, runtimeTestSink(t.TempDir()))
	if err != nil || result.Outcome != scenarioruntime.Passed || len(result.Tests) != 5 {
		t.Fatal("trusted execution failed", result.Outcome, err)
	}
}
