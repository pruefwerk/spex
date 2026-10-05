package spex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/internal/workspace"
)

func summaryFixture(t *testing.T, root, name, result string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(path, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "kuttl-test.yaml"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if result == "" {
		return
	}
	report := ScenarioRunReport{APIVersion: "spex.report.v0.1", Kind: "ScenarioRunReport", Metadata: ReportMetadata{Name: name}, Status: ReportStatus{Result: result, StartedAt: "2026-10-01T00:00:00Z", FinishedAt: "2026-10-01T00:05:00Z"}}
	data, _ := json.Marshal(report)
	if err := os.WriteFile(filepath.Join(path, "reports/scenario-run-report.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReportSummaryMissingFailureAndDuration(t *testing.T) {
	root := t.TempDir()
	summaryFixture(t, root, "passed", "passed")
	summaryFixture(t, root, "failed", "failed")
	summaryFixture(t, root, "interrupted", "")
	var out bytes.Buffer
	if err := Run([]string{"reports", "summarize", "--out", root}, &out, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"passed: passed (300.0s)", "failed: failed", "interrupted: no final report; not proven complete", "not proof that the full suite finished"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
	summary, err := summarizeReports(root)
	if err != nil || summary.Complete || summary.Counts["passed"] != 1 || summary.Counts["missing"] != 1 {
		t.Fatalf("%+v %v", summary, err)
	}
}

func TestReportSummaryEmptyNestedAndMalformed(t *testing.T) {
	root := t.TempDir()
	out, err := summarizeReports(root)
	if err != nil || out.Complete || len(out.Scenarios) != 0 {
		t.Fatalf("%+v %v", out, err)
	}
	summaryFixture(t, filepath.Join(root, "group-a"), "first", "passed")
	out, err = summarizeReports(root)
	if err != nil || !out.Complete {
		t.Fatalf("%+v %v", out, err)
	}
	file := filepath.Join(root, "group-a/first/reports/scenario-run-report.json")
	if err := os.WriteFile(file, []byte("invalid"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = summarizeReports(root)
	if err != nil || out.Complete || out.Counts["error"] != 1 {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestReportSummaryRefusesSymlinkReport(t *testing.T) {
	root := t.TempDir()
	summaryFixture(t, root, "test", "passed")
	path := filepath.Join(root, "test/reports/scenario-run-report.json")
	data, _ := os.ReadFile(path)
	os.Remove(path)
	target := filepath.Join(t.TempDir(), "report")
	os.WriteFile(target, data, 0o644)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	out, err := summarizeReports(root)
	if err != nil || out.Complete || out.Counts["error"] != 1 {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestGroupSummaryDerivesStatusFromExitCode(t *testing.T) {
	root := t.TempDir()
	plan := `{"ok":{"scenarios":2,"selected":true},"timeout":{"scenarios":1,"selected":true},"missing":{"scenarios":1,"selected":true},"other":{"scenarios":1},"empty":{"scenarios":0}}`
	os.WriteFile(filepath.Join(root, "group-plan.json"), []byte(plan), 0o644)
	os.WriteFile(filepath.Join(root, "ok-result.json"), []byte(`{"exitCode":0,"status":"failed"}`), 0o644)
	os.WriteFile(filepath.Join(root, "timeout-result.json"), []byte(`{"exitCode":124,"status":"passed"}`), 0o644)
	var out bytes.Buffer
	err := Run([]string{"reports", "groups", "--root", root}, &out, &out)
	if ExitCode(err) != 1 {
		t.Fatalf("expected incomplete status, got %v", err)
	}
	for _, want := range []string{"| ok | 2 | passed |", "| timeout | 1 | timed out |", "| missing | 1 | not run (blocked or cancelled) |", "| other | 1 | not selected |", "| empty | 0 | unimplemented |"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
}

func TestReportSummaryRejectsReportFromPreviousRun(t *testing.T) {
	root := t.TempDir()
	summaryFixture(t, root, "test", "passed")
	os.WriteFile(filepath.Join(root, "test/scenario-context.json"), []byte(`{"scenario":"test","runId":"new-run"}`), 0o644)
	out, err := summarizeReports(root)
	if err != nil || out.Complete || out.Counts["error"] != 1 {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestReportSummaryUsesGeneratedScenarioIdentity(t *testing.T) {
	names := []string{
		"aws-native-registration-establishes-routing-without--c9c4211973",
		"bootstraps-cannot-reopen-migration-or-roll-back-aws--f40cdb88c9",
		"AWS native registration with spaces_and.dots",
		strings.Repeat("long-scenario-name-", 8),
	}
	for _, source := range names {
		for _, result := range []string{"passed", "failed"} {
			t.Run(source+"/"+result, func(t *testing.T) {
				root := t.TempDir()
				name := workspace.DNSLabel(source)
				summaryFixture(t, root, name, result)
				path := filepath.Join(root, name)
				runID := "same-run"
				data, err := os.ReadFile(filepath.Join(path, "reports/scenario-run-report.json"))
				if err != nil {
					t.Fatal(err)
				}
				var report ScenarioRunReport
				if err := json.Unmarshal(data, &report); err != nil {
					t.Fatal(err)
				}
				report.Metadata.RunID = &runID
				data, _ = json.Marshal(report)
				if err := os.WriteFile(filepath.Join(path, "reports/scenario-run-report.json"), data, 0o644); err != nil {
					t.Fatal(err)
				}
				data, _ = json.Marshal(map[string]string{"scenario": source, "runId": runID})
				if err := os.WriteFile(filepath.Join(path, "scenario-context.json"), data, 0o644); err != nil {
					t.Fatal(err)
				}
				out, err := summarizeReports(root)
				if err != nil || !out.Complete || out.Counts[result] != 1 || out.Counts["error"] != 0 {
					t.Fatalf("valid normalized report rejected or result changed: %+v %v", out, err)
				}
			})
		}
	}
}

func TestReportSummaryRejectsUnrelatedScenarioIdentity(t *testing.T) {
	for _, source := range []string{"", "different--scenario"} {
		root := t.TempDir()
		summaryFixture(t, root, "actual-scenario", "passed")
		data, _ := json.Marshal(map[string]string{"scenario": source})
		if err := os.WriteFile(filepath.Join(root, "actual-scenario/scenario-context.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := summarizeReports(root)
		if err != nil || out.Complete || out.Counts["error"] != 1 {
			t.Fatalf("unrelated report accepted: %+v %v", out, err)
		}
	}
}

func TestGroupSummaryRejectsTraversalAndInvalidResult(t *testing.T) {
	for _, plan := range []string{`{"../escape":{"scenarios":1,"selected":true}}`, `{}`, `{"test":{"scenarios":-1}}`, `{"test":{"scenarios":1,"selected":true}}`} {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, "group-plan.json"), []byte(plan), 0o644)
		os.WriteFile(filepath.Join(root, "test-result.json"), []byte(`{"status":"passed"}`), 0o644)
		if err := runGroupSummary([]string{"--root", root}, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted invalid plan/result: %s", plan)
		}
	}
}
