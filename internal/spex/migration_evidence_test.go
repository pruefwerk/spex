package spex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrationEvidenceRetainsOnlyKnownFailureClasses(t *testing.T) {
	root, artifacts := t.TempDir(), t.TempDir()
	const sentinel = "SECRET_BACKEND_FAILURE"
	known, private := "before_scenario_hook_failed", sentinel
	report := ScenarioRunReport{
		Status: ReportStatus{Result: "error", FailureClass: &known, FailureMessage: &private},
		Steps:  []ReportStep{{Result: "failed", FailureClass: &private, FailureMessage: &private}},
	}
	path := filepath.Join(root, "scenario", "reports", "scenario-run-report.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := persistMigrationEvidence(root, runtimeTestSink(artifacts)); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(artifacts, "artifacts/test-evidence.json"))
	if err != nil || !bytes.Contains(data, []byte(known)) || bytes.Contains(data, []byte(sentinel)) {
		t.Fatal("failure evidence lost its safe class or retained backend data", err)
	}
	for _, code := range []string{"cancelled", "kuttl_execution_failure", "runtime_cleanup_failed"} {
		if safeEvidenceFailureClass(&code) != code {
			t.Fatal("known lifecycle class lost", code)
		}
	}
	if safeEvidenceFailureClass(nil) != "" {
		t.Fatal("missing class changed")
	}
}
