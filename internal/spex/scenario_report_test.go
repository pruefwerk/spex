package spex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

func TestCanonicalReportSummary(t *testing.T) {
	for _, state := range []string{"passed", "failed", "cancelled", "missing", "mismatch", "partial", "cleanup", "multiple", "symlink"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			doc := scenario.Scenario{Schema: scenario.Schema, Runtime: "migration-testbench/v1"}
			canonical, _ := scenario.SerializeCanonical(doc)
			id, _ := scenario.Identity(doc)
			result := scenarioruntime.ExecutionResult{Schema: "spex.result/v1", ScenarioID: id, Runtime: doc.Runtime, Outcome: scenarioruntime.Passed, Cleanup: "succeeded", Tests: []scenarioruntime.TestResult{{Name: "SENTINEL", Outcome: scenarioruntime.Passed}}}
			switch state {
			case "failed", "cancelled":
				result.Outcome = scenarioruntime.Outcome(state)
			case "mismatch":
				result.ScenarioID = "wrong"
			case "partial":
				result.Tests = nil
			case "cleanup":
				result.Problems = []scenarioruntime.Problem{{Message: "SENTINEL"}}
			}
			write := func(name string, data []byte) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write("scenario.toml", canonical)
			data, _ := json.Marshal(result)
			if state != "missing" {
				write("result.json", data)
			}
			data, _ = json.Marshal(scenarioruntime.PlanSummary{Tests: []scenarioruntime.TestDescription{{Name: "SENTINEL"}}})
			write("plan.json", data)
			if state == "multiple" {
				if err := os.Mkdir(filepath.Join(root, "old"), 0o700); err != nil {
					t.Fatal(err)
				}
				write("old/scenario.toml", canonical)
			}
			if state == "symlink" {
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "linked")); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			err := Run([]string{"reports", "scenario", "--out", root}, &output, &output)
			if (err == nil) != (state == "passed") {
				t.Fatalf("state %s: %v", state, err)
			}
			if strings.Contains(output.String(), "SENTINEL") || (err != nil && strings.Contains(err.Error(), "SENTINEL")) {
				t.Fatal("report leaked free-form values")
			}
		})
	}
}
