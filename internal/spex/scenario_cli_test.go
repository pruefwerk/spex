package spex

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/internal/workspace"
	"github.com/pruefwerk/spex/pkg/scenario"
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
		original, _ := os.ReadFile(filepath.Join(dir, mode+".toml"))
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
