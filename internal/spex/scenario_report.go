package spex

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

// Inspect one invocation, not a cache of historical successful executions.
// Print only identity, counts and enumerated outcomes, never backend messages.
func runCanonicalResultSummary(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("reports scenario", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("out", "", "artifact base containing exactly one execution")
	if err := fs.Parse(args); err != nil {
		return errors.New("invalid scenario report arguments")
	}
	if *root == "" || fs.NArg() != 0 {
		return errors.New("reports scenario requires --out")
	}
	info, err := os.Lstat(*root)
	if err != nil || !info.IsDir() {
		return errors.New("scenario report directory unavailable")
	}
	var executions []string
	err = filepath.WalkDir(*root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return errors.New("cannot inspect scenario reports")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("scenario report links are not allowed")
		}
		if entry.Name() == "scenario.toml" {
			executions = append(executions, filepath.Dir(path))
		}
		if len(executions) > 1 {
			return errors.New("expected one scenario execution; use an isolated invocation directory")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(executions) != 1 {
		return errors.New("no scenario execution record; completion is not proven")
	}
	dir := executions[0]
	data, err := readRegularEvidenceFile(filepath.Join(dir, "scenario.toml"), scenario.MaxDocumentBytes)
	if err != nil {
		return errors.New("canonical scenario unavailable")
	}
	doc, err := scenario.Parse(data)
	if err != nil {
		return errors.New("invalid canonical scenario")
	}
	id, err := scenario.Identity(doc)
	if err != nil {
		return errors.New("invalid scenario identity")
	}
	data, err = readRegularEvidenceFile(filepath.Join(dir, "result.json"), maxScenarioReportSummarySize)
	if err != nil {
		return errors.New("no readable final result; completion is not proven")
	}
	var result scenarioruntime.ExecutionResult
	if json.Unmarshal(data, &result) != nil || result.Schema != "spex.result/v1" || result.ScenarioID != id || result.Runtime != doc.Runtime {
		return errors.New("result does not match canonical scenario")
	}
	data, err = readRegularEvidenceFile(filepath.Join(dir, "plan.json"), maxScenarioReportSummarySize)
	var plan scenarioruntime.PlanSummary
	if err != nil || json.Unmarshal(data, &plan) != nil {
		return errors.New("scenario plan unavailable")
	}
	counts := map[scenarioruntime.Outcome]int{}
	for _, test := range result.Tests {
		switch test.Outcome {
		case scenarioruntime.Passed, scenarioruntime.Failed, scenarioruntime.Error, scenarioruntime.Cancelled:
			counts[test.Outcome]++
		default:
			return errors.New("invalid test outcome")
		}
	}
	switch result.Outcome {
	case scenarioruntime.Passed, scenarioruntime.Failed, scenarioruntime.Error, scenarioruntime.Cancelled:
	default:
		return errors.New("invalid scenario outcome")
	}
	fmt.Fprintf(out, "Scenario %s: %s\nTests reported: %d of %d; passed: %d; failed: %d; error: %d; cancelled: %d\n", id, result.Outcome, len(result.Tests), len(plan.Tests), counts[scenarioruntime.Passed], counts[scenarioruntime.Failed], counts[scenarioruntime.Error], counts[scenarioruntime.Cancelled])
	if result.ResourceClaims != nil {
		fmt.Fprintf(out, "Resource claims: %s; resources: %d; wait: %dms\n", result.ResourceClaims.Status, len(result.ResourceClaims.Claims), result.ResourceClaims.WaitMilliseconds)
	}
	if result.Outcome != scenarioruntime.Passed {
		return errors.New("scenario did not pass")
	}
	if len(plan.ResourceClaims) > 0 && (result.ResourceClaims == nil || result.ResourceClaims.Status != "released") {
		return errors.New("resource claims remain unresolved")
	}
	if len(plan.Tests) == 0 || len(result.Tests) != len(plan.Tests) || counts[scenarioruntime.Passed] != len(plan.Tests) || len(result.Problems) > 0 || (result.Cleanup != "succeeded" && result.Cleanup != "not_run") {
		return errors.New("complete successful execution is not proven")
	}
	return nil
}
