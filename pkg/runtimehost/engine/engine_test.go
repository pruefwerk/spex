package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
)

func configuredFixture(t *testing.T) (string, string) {
	t.Helper()
	// Unit tests must never write commands into the enclosing GitHub job.
	for _, key := range []string{"GITHUB_ENV", "GITHUB_OUTPUT", "GITHUB_STEP_SUMMARY"} {
		t.Setenv(key, "")
	}
	root := t.TempDir()
	d := hostconfig.Defaults()
	d.Suite = "suite-kind-example.yaml"
	d.Binding = "bindings/local.yaml"
	d.Profile = "integration/local.yaml"
	d.Topology = "integration/node.yaml"
	d.Namespace = "acceptance"
	d.Groups = []string{"baseline", "change"}
	data, err := toml.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "host.toml")
	if os.WriteFile(file, data, 0600) != nil {
		t.Fatal("definition")
	}
	for ref, content := range map[string]string{
		d.Suite:    "spec:\n  bindingRef: bindings/local.yaml\n  integrationProfileRef: integration/local.yaml\n  workspaceDir: original\n  reports:\n    outputDir: reports\n",
		d.Binding:  "spec:\n  kubeContext: kind-original\n  probe:\n    image: example-probe:fixture\n",
		d.Profile:  "spec:\n  kind:\n    start: true\n    clusterName: original\n    config: integration/node.yaml\n    commands:\n      - command: \"'" + "${repoRoot}" + "/.spex/bin/runtime-host' kind images --root '" + "${repoRoot}" + "'\"\n",
		d.Topology: "kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnodes:\n- role: control-plane\n",
	} {
		path := filepath.Join(root, ref)
		if os.MkdirAll(filepath.Dir(path), 0700) != nil || os.WriteFile(path, []byte(content), 0600) != nil {
			t.Fatal("fixture")
		}
	}
	return root, file
}
func TestExecutableHostUsesOnlyConfiguredSuite(t *testing.T) {
	root, file := configuredFixture(t)
	t.Chdir(root)
	var out, errs bytes.Buffer
	if Run(context.Background(), []string{"--config", file, "execution", "supported", "--suite", "suite-kind-example.yaml"}, &out, &errs, "test") != 0 {
		t.Fatal(errs.String())
	}
	if Run(context.Background(), []string{"--config", file, "execution", "supported", "--suite", "other.yaml"}, &out, &errs, "test") == 0 {
		t.Fatal("unqualified suite accepted")
	}
}
func TestInvalidDefinitionNeverReachesExecution(t *testing.T) {
	root, file := configuredFixture(t)
	t.Chdir(root)
	if os.WriteFile(file, []byte("schema='unknown'"), 0600) != nil {
		t.Fatal("fixture")
	}
	var out, errs bytes.Buffer
	if Run(context.Background(), []string{"--config", file, "execution", "prepare-start"}, &out, &errs, "test") != 2 {
		t.Fatal("invalid definition accepted")
	}
	if _, err := os.Stat(filepath.Join(root, ".spex")); !os.IsNotExist(err) {
		t.Fatal("invalid definition wrote execution state")
	}
}
func TestProjectionPreservesTemplatesAndUsesConfiguredPaths(t *testing.T) {
	root, file := configuredFixture(t)
	t.Chdir(root)
	d, err := hostconfig.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(file, "test")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, d.Suite))
	if err != nil {
		t.Fatal(err)
	}
	paths, err := h.prepareExecution(root, d.Suite, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.checkExecution(root, paths, nil); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, d.Suite))
	if string(before) != string(after) || !strings.Contains(paths.Scope, "spex-") {
		t.Fatal("template mutated or project prefix used")
	}
}
func TestConfiguredAdmissionTagsAreNotHardCoded(t *testing.T) {
	root, file := configuredFixture(t)
	t.Chdir(root)
	d, err := hostconfig.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(file, "test")
	if err != nil {
		t.Fatal(err)
	}
	tags := h.allowedGroupTags()
	if !tags["group-change"] || !tags[d.BaselineTag] || tags["group-modbus"] {
		t.Fatal(tags)
	}
}

func TestPreparedExecutionRejectsPolicyDrift(t *testing.T) {
	root, file := configuredFixture(t)
	t.Chdir(root)
	d, err := hostconfig.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(file, "test")
	if err != nil {
		t.Fatal(err)
	}
	paths, err := h.prepareExecution(root, d.Suite, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	d.MaximumTimeout = "1h"
	updated, _ := toml.Marshal(d)
	if err := os.WriteFile(file, updated, 0600); err != nil {
		t.Fatal(err)
	}
	h, err = New(file, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.checkExecution(root, paths, nil); err == nil {
		t.Fatal("changed policy admitted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(paths.Kubeconfig), "creation.json")); !os.IsNotExist(err) {
		t.Fatal("policy drift created environment")
	}
}

func TestPreparationUsesDeclaredEnvironmentKey(t *testing.T) {
	root, file := configuredFixture(t)
	t.Chdir(root)
	var out, errs bytes.Buffer
	if Run(context.Background(), []string{"--config", file, "execution", "prepare"}, &out, &errs, "test") != 0 {
		t.Fatal(errs.String())
	}
	if !strings.Contains(out.String(), "SPEX_EXECUTION_SCOPE") || strings.Contains(out.String(), "MTB_SCOPE_FILE") {
		t.Fatal(out.String())
	}
}

func TestFixtureDoesNotWriteEnclosingGitHubEnvironment(t *testing.T) {
	file := filepath.Join(t.TempDir(), "runner-env")
	const original = "EXISTING=value\n"
	if err := os.WriteFile(file, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_ENV", file)
	root, config := configuredFixture(t)
	t.Chdir(root)
	var out bytes.Buffer
	if Run(context.Background(), []string{"--config", config, "execution", "prepare"}, &out, &out, "test") != 0 {
		t.Fatal(out.String())
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != original {
		t.Fatal("fixture changed enclosing GitHub job", err)
	}
}
