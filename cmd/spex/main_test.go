package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/pruefwerk/spex/pkg/runtimehost/definition"
)

var executable string

// These checks enter the built executable, not an internal dispatch function.
// Release qualification supplies its actual binary through SPEX_TEST_BINARY.
func TestMain(m *testing.M) {
	executable = os.Getenv("SPEX_TEST_BINARY")
	if executable != "" {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "spex-cli-tests-")
	if err != nil {
		panic(err)
	}
	executable = filepath.Join(dir, "spex")
	build := exec.Command("go", "build", "-o", executable, ".")
	if out, err := build.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		panic(string(out))
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func invoke(t *testing.T, root string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(executable, args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return string(out), exit.ExitCode()
	}
	t.Fatal(err)
	return "", -1
}

func TestBinaryCommandDispatch(t *testing.T) {
	root := t.TempDir()
	out, code := invoke(t, root, "version", "--format", "json")
	var version struct{ Version string }
	if code != 0 || json.Unmarshal([]byte(out), &version) != nil || version.Version == "" {
		t.Fatal(out, code)
	}
	out, code = invoke(t, root, "runtime", "--help")
	if code != 0 || !strings.Contains(out, "spex runtime") {
		t.Fatal(out, code)
	}
}

func TestBinaryRuntimeValidationAndSelection(t *testing.T) {
	root := t.TempDir()
	policy := definition.Defaults()
	policy.Suite = "suite.yaml"
	policy.Binding = "bindings/local.yaml"
	policy.Profile = "integration/profile.yaml"
	policy.Topology = "integration/kind.yaml"
	policy.Namespace = "acceptance"
	data, err := toml.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "host.toml")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	out, code := invoke(t, root, "runtime", "--config", file, "validate")
	if code != 0 || !strings.Contains(out, "definition valid") {
		t.Fatal(out, code)
	}
	out, code = invoke(t, root, "runtime", "--config", file, "config", "select", "--event", "push", "--message", "Change [kind:gateway-migration][kind-group:modbus,rollback]")
	var selected struct{ Kinds, Groups string }
	if code != 0 || json.Unmarshal([]byte(out), &selected) != nil || selected.Kinds != "gateway-migration" || selected.Groups != "modbus,rollback" {
		t.Fatal(out, code)
	}
	out, code = invoke(t, root, "runtime", "--config", file, "config", "select", "--event", "workflow_dispatch", "--kinds", "example", "--groups", "one,two")
	if code != 0 || json.Unmarshal([]byte(out), &selected) != nil || selected.Kinds != "example" || selected.Groups != "one,two" {
		t.Fatal(out, code)
	}
	if err := os.WriteFile(file, []byte("schema='unsupported'"), 0600); err != nil {
		t.Fatal(err)
	}
	_, code = invoke(t, root, "runtime", "--config", file, "execution", "prepare-start")
	if code != 2 {
		t.Fatal("invalid configuration exit", code)
	}
	if _, err := os.Stat(filepath.Join(root, ".spex")); !os.IsNotExist(err) {
		t.Fatal("invalid input mutated environment")
	}
}
