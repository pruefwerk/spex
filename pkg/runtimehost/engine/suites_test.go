package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
)

func TestRegisteredSuiteUsesItsOwnConfigurationAndRetainsOwnership(t *testing.T) {
	root, file := configuredFixture(t)
	t.Chdir(root)
	t.Setenv(hostconfig.SuiteEnvironment, "")
	d, err := hostconfig.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	for original, target := range map[string]string{d.Suite: "suite-kind-other.yaml", d.Binding: "bindings/other.yaml", d.Profile: "integration/other.yaml", d.Topology: "integration/other-node.yaml"} {
		content, err := os.ReadFile(filepath.Join(root, original))
		if err != nil {
			t.Fatal(err)
		}
		for from, to := range map[string]string{d.Binding: "bindings/other.yaml", d.Profile: "integration/other.yaml", d.Topology: "integration/other-node.yaml"} {
			content = []byte(strings.ReplaceAll(string(content), from, to))
		}
		if err := os.WriteFile(filepath.Join(root, target), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	d.Suites = []hostconfig.SuitePolicy{{Suite: "suite-kind-other.yaml"}}
	data, err := toml.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	h, err := New(file, "test")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := h.forSuite("suite-kind-other.yaml")
	if err != nil || selected.config.Binding != "bindings/other.yaml" || selected.config.Profile != "integration/other.yaml" || selected.config.Topology != "integration/other-node.yaml" {
		t.Fatal("suite references were not resolved", err)
	}
	var output bytes.Buffer
	if code := h.Run(context.Background(), []string{"execution", "prepare", "--suite", "suite-kind-other.yaml"}, &output, &output); code != 0 {
		t.Fatal(output.String())
	}
	var paths executionPaths
	var values map[string]string
	if err := json.Unmarshal(output.Bytes(), &values); err != nil {
		t.Fatal(err)
	}
	paths = executionPaths{values[d.ScopeEnvironment], values["SPEX_SUITE"], values["KUBECONFIG"]}
	if _, err := selected.checkExecution(root, paths, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.checkExecution(root, paths, nil); err == nil {
		t.Fatal("default suite adopted another suite's execution")
	}
	environment := filepath.Join(root, "github-env")
	if err := selected.writeExecutionEnvironment(environment, paths); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(environment)
	if !strings.Contains(string(data), hostconfig.SuiteEnvironment+"=suite-kind-other.yaml\n") {
		t.Fatal("CI did not retain original suite selection")
	}
	t.Setenv(hostconfig.SuiteEnvironment, "suite-kind-other.yaml")
	child, err := New(file, "test")
	if err != nil {
		t.Fatal(err)
	}
	child, err = child.forOperation([]string{"execution", "check", "--suite", paths.Suite})
	if err != nil || hostconfig.Identity(child.config) != hostconfig.Identity(selected.config) {
		t.Fatal("profile child did not retain suite policy", err)
	}
	if h.config.Suite != d.Suite {
		t.Fatal("selection mutated the original host")
	}
	t.Setenv(hostconfig.SuiteEnvironment, "")
	t.Setenv("SPEX_SUITE", "suite-kind-other.yaml")
	fromEnvironment, err := h.forOperation([]string{"execution", "supported"})
	if err != nil || hostconfig.Identity(fromEnvironment.config) != hostconfig.Identity(selected.config) {
		t.Fatal("initial SPEX_SUITE selection was not honored", err)
	}
}
