package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/pkg/migrationtestbench"
)

func TestCIWholeSuiteResultDoesNotRequireASubgroup(t *testing.T) {
	_, file := configuredFixture(t)
	h, err := New(file, "test")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var out bytes.Buffer
	if err := h.ciCommand(context.Background(), []string{"result", "--root", root, "--group", "suite", "--status", "0"}, &out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "suite-result.json"))
	if err != nil || !strings.Contains(string(data), `"status":"passed"`) {
		t.Fatal("whole-suite result missing", err)
	}
}

func TestCIPlanSelectsConfiguredBaselineAlongsideFeatureGroup(t *testing.T) {
	_, file := configuredFixture(t)
	h, err := New(file, "test")
	if err != nil {
		t.Fatal(err)
	}
	h.config.BaselineTag = "host-smoke"
	tests := []migrationtestbench.SuiteTest{
		{Tags: []string{"host-smoke"}},
		{Tags: []string{"group-change"}},
	}
	for _, selection := range []string{"baseline", "baseline,change", ""} {
		plan, err := h.planGroups(tests, selection, map[string]bool{"change": true})
		if err != nil {
			t.Fatal(err)
		}
		if !plan["baseline"].Selected || plan["baseline"].Scenarios != 1 || plan["change"].Selected != (selection != "baseline") {
			t.Fatalf("selection %q: %+v", selection, plan)
		}
	}
}

func TestCISelectionDiagnosticsDoNotExposeOtherErrors(t *testing.T) {
	root, file := configuredFixture(t)
	t.Chdir(root)
	// Discovery needs a valid suite, but group diagnostics run before compiling
	// its steps or touching an acceptance environment.
	for path, content := range map[string]string{
		"suite-kind-example.yaml": "apiVersion: spex.suite.v0.1\nkind: ScenarioSuite\nmetadata:\n  name: example\nspec:\n  bindingRef: binding.yaml\n  scenarios: [scenario.yaml]\n",
		"binding.yaml":            "apiVersion: spex.binding.v0.1\nkind: TargetBinding\nmetadata:\n  name: local\nspec:\n  namespace: acceptance\n  rbac:\n    create: true\n  probe:\n    image: probe:test\n  mqtt:\n    brokerURL: tcp://localhost:1883\n",
		"scenario.yaml": `apiVersion: spex.scenario.v0.1
kind: Scenario
metadata:
  name: baseline
  tags: [ci-baseline]
spec:
  payloadTemplates:
    sample:
      contentType: application/json
      body: '{"scenarioRunId":"${scenarioRunId}","correlationId":"${correlationId}"}'
  operations:
    - id: publish
      type: mqtt.publish
      mqtt:
        topic: fixture
        payloadTemplateRef: sample
        correlationId: sample
`,
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := migrationtestbench.DiscoverSuite(context.Background(), "suite-kind-example.yaml"); err != nil {
		t.Fatal("discovery fixture:", err)
	}
	for _, operation := range []string{"plan", "validate"} {
		var selectedOut, selectedErrs bytes.Buffer
		code := Run(context.Background(), []string{"--config", file, "ci", operation, "--groups", "baseline", "--root", "reports"}, &selectedOut, &selectedErrs, "test")
		if code != 0 {
			t.Fatalf("%s baseline: code=%d stdout=%q stderr=%q", operation, code, selectedOut.String(), selectedErrs.String())
		}
		if operation == "validate" && !strings.Contains(selectedOut.String(), "suite validation passed: 1 scenario(s)") {
			t.Fatal(selectedOut.String())
		}
		var out, errs bytes.Buffer
		code = Run(context.Background(), []string{"--config", file, "ci", operation, "--groups", "unknown"}, &out, &errs, "test")
		if code != 1 || !strings.Contains(errs.String(), "CI group selection failed: unknown groups: unknown") {
			t.Fatalf("%s: code=%d stdout=%q stderr=%q", operation, code, out.String(), errs.String())
		}
	}
	const secret = "PRIVATE-CONFIG-SENTINEL"
	if err := os.WriteFile(filepath.Join(root, "suite-kind-example.yaml"), []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	code := Run(context.Background(), []string{"--config", file, "ci", "validate"}, &out, &errs, "test")
	if code != 1 || strings.Contains(out.String()+errs.String(), secret) || !strings.Contains(errs.String(), "CI planning, validation or reporting failed") {
		t.Fatalf("private error: code=%d stdout=%q stderr=%q", code, out.String(), errs.String())
	}
}
