package spex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/internal/migrationresources"
	"github.com/pruefwerk/spex/internal/workspace"
	"github.com/pruefwerk/spex/pkg/resourceclaims"
	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

const scenarioSmokeSource = `apiVersion: spex.scenario.v0.1
kind: Scenario
metadata:
  name: source-smoke
spec:
  parameters:
    tenantId:
      type: string
      default: tenant-dev
    deviceId:
      type: string
      default: device-dev-1
  defaults:
    timeout: 60s
    pollInterval: 1s
  correlation:
    scenarioRunId: auto
    strategy: payloadTemplate
  payloadTemplates:
    sample:
      contentType: application/json
      body: '{"scenarioRunId":"${scenarioRunId}","correlationId":"${correlationId}"}'
  operations:
    - id: publish
      type: mqtt.publish
      mqtt:
        topic: telemetry/test
        payloadTemplateRef: sample
        correlationId: one
`

func TestScenarioCLIAuthoringModes(t *testing.T) {
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()
	t.Setenv("SPEX_SUITE", filepath.Join(root, "examples/suites/mqtt-local.yaml"))
	dir := t.TempDir()
	if _, err := workspace.LoadSourceInputs([]byte(scenarioSmokeSource), filepath.Join(dir, "inline.spex"), filepath.Join(root, "examples/bindings/local-dev.yaml"), workspace.CatalogBundle{}, nil); err != nil {
		t.Fatal(err)
	}
	file := "test;echo injected.spex"
	if err := os.WriteFile(filepath.Join(dir, file), []byte(scenarioSmokeSource), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := Run(append([]string{"scenario"}, args...), &out, &out); err != nil {
			t.Fatalf("CLI failed: %v\n%s", err, out.String())
		}
		return out.String()
	}
	for _, mode := range []string{"inline", "file"} {
		flag := "--spex-file"
		if mode == "inline" {
			flag = "--inline-file"
		}
		name := "name; $(echo not-executed)"
		run("build", "--workspace", dir, "--runtime", "migration-testbench/v1", "--name", name, flag, file, "--out", mode+".toml")
		if !strings.Contains(run("validate", "--workspace", dir, mode+".toml"), "scenario valid:") {
			t.Fatal("missing validation result")
		}
		if !strings.Contains(run("explain", "--workspace", dir, mode+".toml"), "source-smoke") {
			t.Fatal("missing plan")
		}
		run("build", "--workspace", dir, "--scenario", mode+".toml", "--out", mode+"-copy.toml")
		run("build", "--workspace", dir, "--definition-file", mode+".toml", "--out", mode+"-definition.toml")
		original, _ := os.ReadFile(filepath.Join(dir, mode+".toml"))
		unified, _ := os.ReadFile(filepath.Join(dir, mode+"-definition.toml"))
		if !bytes.Equal(original, unified) {
			t.Fatal("unified TOML authoring changed scenario identity")
		}
		copied, _ := os.ReadFile(filepath.Join(dir, mode+"-copy.toml"))
		if !bytes.Equal(original, copied) {
			t.Fatal("committed scenario changed canonical bytes")
		}
		parsed, err := scenario.Parse(original)
		if err != nil || parsed.Metadata.Name != name {
			t.Fatal("shell-looking metadata changed")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "override.toml"), []byte("[execution]\nfail_fast = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("build", "--workspace", dir, "--scenario", "inline.toml", "--runtime-config-file", "override.toml", "--out", "override-result.toml")
	feature := `Feature: Inline smoke
  Background:
    Given tenant "tenant-dev"
    And device "device-dev-1"
  Scenario: Publish inline telemetry
    When device "device-dev-1" publishes energy reading 42.5 as "reading-1"
`
	if err := os.WriteFile(filepath.Join(dir, "smoke.feature"), []byte(feature), 0o600); err != nil {
		t.Fatal(err)
	}
	run("build", "--workspace", dir, "--runtime", "migration-testbench/v1", "--inline-file", "smoke.feature", "--out", "gherkin.toml")
	if err := os.WriteFile(filepath.Join(dir, "mixed.txt"), []byte(feature+"---\n"+scenarioSmokeSource), 0o600); err != nil {
		t.Fatal(err)
	}
	run("build", "--workspace", dir, "--runtime", "migration-testbench/v1", "--inline-file", "mixed.txt", "--out", "mixed.toml")
	run("validate", "--workspace", dir, "mixed.toml")
	mixedPlan := run("explain", "--workspace", dir, "mixed.toml")
	for _, label := range []string{"scenario.toml:test[0]", "scenario.toml:test[1]"} {
		if !strings.Contains(mixedPlan, label) {
			t.Fatalf("mixed source missing from plan: %s", label)
		}
	}
	if !strings.Contains(run("explain", "--workspace", dir, "gherkin.toml"), "scenario.toml:test[0]") {
		t.Fatal("inline plan exposed an unstable location")
	}
	for _, args := range [][]string{
		{"build", "--scenario", "inline.toml", "--inline-file", file},
		{"build", "--scenario", "inline.toml", "--runtime", "other/v1"},
		{"build", "--runtime", "migration-testbench/v1", "--spex-file", "../outside.spex"},
		{"build", "--scenario", "inline.toml", "--out", "inline.toml"},
	} {
		var out bytes.Buffer
		command := append([]string{"scenario", args[0], "--workspace", dir}, args[1:]...)
		if err := Run(command, &out, &out); err == nil {
			t.Fatal("accepted ambiguous, escaping or destructive authoring")
		}
	}
}

func TestScenarioCLIHostResourceContract(t *testing.T) {
	root := repoRoot(t)
	t.Chdir(root)
	t.Setenv("SPEX_SUITE", filepath.Join(root, "examples/suites/mqtt-local.yaml"))
	directory := t.TempDir()
	inline := scenarioSmokeSource
	doc := scenario.Scenario{Schema: scenario.Schema, Runtime: "migration-testbench/v1", Tests: []scenario.TestSource{{Inline: &inline}}}
	canonical, _ := scenario.SerializeCanonical(doc)
	os.WriteFile(filepath.Join(directory, "scenario.toml"), canonical, 0600)
	var out bytes.Buffer
	if err := Run([]string{"scenario", "explain", "--workspace", directory, "scenario.toml"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	var explained struct {
		ID   string                      `json:"scenario_id"`
		Plan scenarioruntime.PlanSummary `json:"plan"`
	}
	if err := json.Unmarshal(out.Bytes(), &explained); err != nil {
		t.Fatal(err)
	}
	contract := migrationresources.ResourceContract{Schema: "mtb.resource-contract/v1", ScenarioID: explained.ID, Tests: explained.Plan.Tests}
	for _, mode := range []string{"private", "shared-without-coordinator", "wrong-scenario", "unknown-field"} {
		value := contract
		if mode == "shared-without-coordinator" {
			value.Claims = []resourceclaims.Claim{{Resource: "aws/dev/gateway/a", Access: resourceclaims.Exclusive}}
		}
		if mode == "wrong-scenario" {
			value.ScenarioID = strings.Repeat("a", 64)
		}
		data, _ := json.Marshal(value)
		if mode == "unknown-field" {
			data = append(data[:len(data)-1], []byte(`,"typo":true}`)...)
		}
		os.WriteFile(filepath.Join(directory, "contract.json"), data, 0600)
		out.Reset()
		err := Run([]string{"scenario", "explain", "--workspace", directory, "--resource-contract", "contract.json", "scenario.toml"}, &out, &out)
		if (err == nil) != (mode == "private") {
			t.Fatalf("%s: %v", mode, err)
		}
	}
}

func TestCanonicalOutputRejectsSymlinkEscape(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := writeCanonicalScenario(dir, "escape/scenario.toml", []byte("unsafe")); err == nil {
		t.Fatal("output escaped workspace")
	}
	if _, err := os.Stat(filepath.Join(outside, "scenario.toml")); !os.IsNotExist(err) {
		t.Fatal("wrote outside workspace")
	}
}

func TestScenarioCLIFailureDoesNotPersistInput(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SPEX_SUITE", filepath.Join(repoRoot(t), "examples/suites/mqtt-local.yaml"))
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(`password = "SENTINEL_PRIVATE_VALUE"`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Run([]string{"scenario", "build", "--workspace", dir, "--runtime", "migration-testbench/v1", "--runtime-config-file", "config.toml"}, &out, &out)
	if err == nil || strings.Contains(err.Error()+out.String(), "SENTINEL_PRIVATE_VALUE") {
		t.Fatal("missing or unsafe validation failure")
	}
	if _, err := os.Stat(filepath.Join(dir, ".spex")); !os.IsNotExist(err) {
		t.Fatal("invalid input produced artifacts")
	}
}
