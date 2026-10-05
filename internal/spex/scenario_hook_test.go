package spex

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeScenarioHook(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hook")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestScenarioHookRenewsEveryScenarioWithoutPersistingSecrets(t *testing.T) {
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()
	t.Setenv("SPEX_TEST_SECRET", "parent-value")
	hook := writeScenarioHook(t, `printf '{"SPEX_TEST_SECRET":"renewed-private-value","SPEX_TEST_WORKSPACE":"%s"}' "$SPEX_SCENARIO_WORKSPACE"`)
	command := writeScenarioHook(t, `
test "$SPEX_TEST_SECRET" = renewed-private-value || exit 77
if [ "$1" = kuttl ]; then
  test "$(cd "$SPEX_TEST_WORKSPACE" && pwd -P)" = "$(pwd -P)" || exit 78
  echo executed
fi
`)
	out := filepath.Join(t.TempDir(), "suite")
	var stdout, stderr bytes.Buffer
	err := Run([]string{"suite", "run", "--suite", "examples/suites/mqtt-local.yaml", "--out", out,
		"--command", command, "--before-scenario-hook", hook}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("suite failed: %v\nstdout: %s\nstderr: %s", err, &stdout, &stderr)
	}
	if strings.Count(stdout.String(), "executed") != 5 {
		t.Fatal("hook environment did not reach every scenario")
	}
	if os.Getenv("SPEX_TEST_SECRET") != "parent-value" {
		t.Fatal("hook mutated the parent environment")
	}
	if strings.Contains(stdout.String()+stderr.String(), "renewed-private-value") {
		t.Fatal("hook secret leaked to output")
	}
	err = filepath.WalkDir(out, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte("renewed-private-value")) {
				t.Fatalf("hook secret persisted to %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestScenarioHookFailureSkipsExecutionAndWritesSafeReport(t *testing.T) {
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()
	hook := writeScenarioHook(t, "echo private-token >&2\necho private-token\nexit 1\n")
	marker := filepath.Join(t.TempDir(), "executed")
	command := writeScenarioHook(t, "touch '"+marker+"'\n")
	out := filepath.Join(t.TempDir(), "suite")
	var stdout, stderr bytes.Buffer
	err := Run([]string{"suite", "run", "--suite", "examples/suites/mqtt-local.yaml", "--out", out,
		"--command", command, "--before-scenario-hook", hook, "--fail-fast"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("hook failure did not fail suite")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("KUTTL executed after hook failure")
	}
	if strings.Contains(stdout.String()+stderr.String(), "private-token") {
		t.Fatal("failed hook output leaked")
	}
	paths, _ := filepath.Glob(filepath.Join(out, "*", "reports", "scenario-run-report.json"))
	if len(paths) != 1 {
		t.Fatalf("expected one failure report: %v", paths)
	}
	data, _ := os.ReadFile(paths[0])
	if !bytes.Contains(data, []byte("before_scenario_hook_failed")) || bytes.Contains(data, []byte("private-token")) {
		t.Fatal("missing or unsafe hook failure report")
	}
}

func TestScenarioHookRejectsInvalidOutputAndTimeout(t *testing.T) {
	for _, body := range []string{"printf 'null'", "printf '[]'", "printf '{\"BAD-NAME\":\"secret\"}'", "printf '{\"KEY\":123}'", `printf '{"KEY":"'; head -c 70000 /dev/zero | tr '\000' x; printf '"}'`} {
		hook := writeScenarioHook(t, body)
		if _, err := executeScenarioHook(t.TempDir(), hook, time.Second); err == nil {
			t.Fatalf("accepted invalid hook: %s", body)
		}
	}
	if _, err := executeScenarioHook(t.TempDir(), writeScenarioHook(t, "exec sleep 5"), 30*time.Millisecond); err == nil {
		t.Fatal("hook exceeded its deadline")
	}
}

func TestScenarioHookFlagValidation(t *testing.T) {
	for _, command := range []string{"compile", "validate", "list"} {
		if _, err := parseSuiteFlags(command, []string{"--suite", "example.yaml", "--before-scenario-hook", "hook"}); err == nil {
			t.Fatalf("accepted hook for suite %s", command)
		}
	}
	for _, timeout := range []string{"0s", "-1s", "11m"} {
		if _, err := parseSuiteFlags("run", []string{"--suite", "example.yaml", "--before-scenario-hook", "hook", "--before-scenario-hook-timeout", timeout}); err == nil {
			t.Fatalf("accepted invalid hook timeout %s", timeout)
		}
	}
}

func TestScenarioHookEnvironmentIsolation(t *testing.T) {
	hook := writeScenarioHook(t, `printf '{"ISOLATED_WORKSPACE":"%s"}' "$SPEX_SCENARIO_WORKSPACE"`)
	paths := []string{t.TempDir(), t.TempDir()}
	results := make(chan []string, 2)
	for _, path := range paths {
		go func(path string) {
			environment, err := executeScenarioHook(path, hook, time.Second)
			if err != nil {
				results <- nil
				return
			}
			results <- environment
		}(path)
	}
	seen := map[string]bool{}
	for range paths {
		env := <-results
		if len(env) != 1 {
			t.Fatal("hook failed")
		}
		seen[env[0]] = true
	}
	for _, path := range paths {
		if !seen["ISOLATED_WORKSPACE="+path] {
			t.Fatal("concurrent hook environments mixed")
		}
	}
}
