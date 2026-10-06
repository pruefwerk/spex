package spex

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pruefwerk/spex/pkg/scenario"
)

func TestScenarioRunPersistsOnlySafeArtifacts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SPEX_SUITE", filepath.Join(repoRoot(t), "examples/suites/mqtt-local.yaml"))
	const sentinel = "SENTINEL_PRIVATE_BACKEND_RESPONSE"
	t.Setenv("SECRET_TOKEN", sentinel)
	fake := writeFakeKubectl(t, 0, sentinel)
	command, err := os.ReadFile(fake)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), command, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	inline := scenarioSmokeSource
	document := scenario.Scenario{Schema: scenario.Schema, Runtime: "migration-testbench/v1", Tests: []scenario.TestSource{{Inline: &inline}}}
	canonical, err := scenario.SerializeCanonical(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "input.toml"), canonical, 0o600); err != nil {
		t.Fatal(err)
	}
	var previous string
	for i := 0; i < 2; i++ {
		var output bytes.Buffer
		if err := Run([]string{"scenario", "run", "--workspace", dir, "input.toml"}, &output, &output); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), sentinel) {
			t.Fatal("raw backend output reached CLI")
		}
		var summary struct {
			ArtifactDirectory string `json:"artifact_directory"`
			ScenarioID        string `json:"scenario_id"`
			Outcome           string `json:"outcome"`
		}
		if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
			t.Fatal(err)
		}
		if summary.Outcome != "passed" || summary.ArtifactDirectory == previous {
			t.Fatal("invalid result or overwritten rerun")
		}
		previous = summary.ArtifactDirectory
		wantID, _ := scenario.Identity(document)
		if summary.ScenarioID != wantID {
			t.Fatal("execution changed semantic identity")
		}
		count := 0
		err := filepath.WalkDir(summary.ArtifactDirectory, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			count++
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), sentinel) {
				t.Fatal("backend secret persisted")
			}
			if entry.Name() == "kubeconfig" || strings.HasSuffix(entry.Name(), ".yaml") {
				t.Fatal("raw workspace persisted")
			}
			return nil
		})
		if err != nil || count != 5 {
			t.Fatalf("unexpected artifacts: count=%d err=%v", count, err)
		}
	}
}

func TestScenarioRunCancellationWritesResult(t *testing.T) {
	dir, bin := t.TempDir(), t.TempDir()
	t.Setenv("SPEX_SUITE", filepath.Join(repoRoot(t), "examples/suites/mqtt-local.yaml"))
	command := "#!/bin/sh\ncase \"$1\" in kuttl) exec sleep 30;; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(command), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	inline, timeout := scenarioSmokeSource, "100ms"
	data, err := scenario.SerializeCanonical(scenario.Scenario{Schema: scenario.Schema, Runtime: "migration-testbench/v1", Metadata: scenario.Metadata{Timeout: &timeout}, Tests: []scenario.TestSource{{Inline: &inline}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "input.toml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	err = runScenarioCommandContext(ctx, []string{"run", "--workspace", dir, "input.toml"}, &output)
	if ExitCode(err) != 130 {
		t.Fatalf("expected cancellation, got %v", err)
	}
	var summary struct {
		ResultPath string `json:"result_path"`
	}
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(summary.ResultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(result, []byte(`"outcome": "cancelled"`)) {
		t.Fatalf("incorrect cancellation result: %s", result)
	}
}

func TestCleanupFailureDoesNotReplacePrimaryFailure(t *testing.T) {
	dir := writeWorkspace(t)
	command := filepath.Join(t.TempDir(), "kubectl")
	content := "#!/bin/sh\ncase \"$*\" in kuttl*) echo primary-failure; exit 7;; *'delete job'*) echo cleanup-failure; exit 1;; esac\nexit 0\n"
	if err := os.WriteFile(command, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runWorkspace([]string{"--workspace", dir, "--command", command}, &output, &output); err == nil {
		t.Fatal("failure disappeared")
	}
	data, err := os.ReadFile(filepath.Join(dir, "reports/scenario-run-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report ScenarioRunReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if !report.Status.CleanupFailed || report.Status.FailureClass == nil || *report.Status.FailureClass == "runtime_cleanup_failed" || report.Status.ScenarioResult != "failed" {
		t.Fatalf("lost primary or secondary failure: %+v", report.Status)
	}
}
