package spex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

func TestRunnerDiagnosticUsesFixedVocabulary(t *testing.T) {
	for _, tt := range []struct {
		output, phase, reason string
		command               int
	}{
		{`error: unknown command "kuttl" for "kubectl" SECRET`, "harness_setup", "kuttl_plugin_unavailable", 0},
		{"harness.go: fatal error getting client: SECRET", "harness_setup", "kubernetes_client_failed", 0},
		{"logger.go |  | running command: [first SECRET]\nlogger.go |  | running command: [second SECRET]\nexit status 1", "profile_setup", "kuttl_failed", 2},
		{"logger | test/1-integration-setup | starting test step 1-integration-setup\nconnection refused SECRET", "integration_setup", "connection_refused", 0},
		{"logger | test/0-rerun-cleanup | starting test step 0-rerun-cleanup\nSECRET", "rerun_cleanup", "kuttl_failed", 0},
		{"logger | test/2-rbac | starting test step 2-rbac", "static_setup", "kuttl_failed", 0},
		{"logger | test/3-op-check | starting test step 3-op-check", "test_execution", "kuttl_failed", 0},
		{"Set kubectl context to SECRET\nRelease SECRET does not exist. Installing it now.\nlogger | test/3-op-check | starting test step 3-op-check", "test_execution", "kuttl_failed", 0},
		{`error: context "SECRET" does not exist`, "harness_setup", "kube_context_unavailable", 0},
	} {
		code := 1
		got := diagnoseRunner(tt.output, &code)
		if got.Phase != tt.phase || got.Reason != tt.reason || got.LastProfileCommand != tt.command || got.ExitCode == nil || *got.ExitCode != 1 {
			t.Fatalf("unexpected diagnostic: %+v", got)
		}
		data, _ := json.Marshal(got)
		if bytes.Contains(data, []byte("SECRET")) {
			t.Fatal("runner output leaked")
		}
	}
	if safeRunnerDiagnostic(&RunnerDiagnostic{Phase: "SECRET", Reason: "SECRET"}) != nil {
		t.Fatal("untrusted phase admitted")
	}
	negative := -99
	d := safeRunnerDiagnostic(&RunnerDiagnostic{Phase: "profile_setup", Reason: "SECRET", LastProfileCommand: 99999, ExitCode: &negative})
	if d.Reason != "kuttl_failed" || d.LastProfileCommand != 0 || d.ExitCode != nil {
		t.Fatal(d)
	}
}

func TestRuntimeFailureRetainsSafeRunnerDiagnostic(t *testing.T) {
	root := repoRoot(t)
	t.Chdir(root)
	output := "logger.go |  | running command: [private preflight SECRET_BACKEND]\npermission denied SECRET_BACKEND"
	flags, err := parseSuiteFlags("run", []string{"--suite", filepath.Join(root, "examples/suites/mqtt-local.yaml"), "--command", writeFakeKubectl(t, 1, output)})
	if err != nil {
		t.Fatal(err)
	}
	r := migrationRuntime{flags: flags}
	prepared, err := r.Resolve(context.Background(), scenarioruntime.ResolveRequest{Workspace: root, Scenario: scenario.Scenario{Schema: scenario.Schema, Runtime: r.ID()}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := prepared.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sink := runtimeTestSink(t.TempDir())
	result, err := prepared.Execute(context.Background(), plan, sink)
	if err == nil || result.Outcome != scenarioruntime.Error {
		t.Fatalf("%+v, %v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(sink.Directory(), "artifacts/test-evidence.json"))
	if err != nil || !bytes.Contains(data, []byte(`"phase":"profile_setup"`)) || !bytes.Contains(data, []byte(`"exit_code":1`)) || bytes.Contains(data, []byte("SECRET_BACKEND")) {
		t.Fatalf("invalid safe evidence: %s; %v", data, err)
	}
	summary, _ := json.Marshal(result)
	if !bytes.Contains(summary, []byte("Runner phase: profile_setup")) || bytes.Contains(summary, []byte("SECRET_BACKEND")) {
		t.Fatalf("invalid result diagnostics: %s", summary)
	}
	// Confirm raw runner output still remains private even when a direct runner
	// writes arbitrary backend output into its capture.
	var raw bytes.Buffer
	runner := executeKUTTL(flags.command, t.TempDir(), "", &raw, io.Discard)
	if runner.Err == nil || !strings.Contains(raw.String(), "SECRET_BACKEND") {
		t.Fatal("failure fixture did not exercise raw output")
	}
}
