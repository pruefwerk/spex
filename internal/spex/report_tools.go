package spex

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pruefwerk/spex/internal/workspace"
)

type reportSummaryRow struct {
	Name            string   `json:"name"`
	Result          string   `json:"result"`
	DurationSeconds *float64 `json:"durationSeconds,omitempty"`
	FailureMessage  string   `json:"failureMessage,omitempty"`
}

type reportSummary struct {
	Scenarios []reportSummaryRow `json:"scenarios"`
	Counts    map[string]int     `json:"counts"`
	Complete  bool               `json:"workspaceReportsComplete"`
}

func runReportTools(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("reports requires summarize or groups")
	}
	if args[0] == "groups" {
		return runGroupSummary(args[1:], stdout)
	}
	if args[0] != "summarize" {
		return fmt.Errorf("unknown reports command %q", args[0])
	}
	fs := flag.NewFlagSet("reports summarize", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("out", "", "generated workspace root")
	format := fs.String("format", "text", "text or json")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "reports summarize"); err != nil {
		return err
	}
	if *root == "" {
		return fmt.Errorf("reports summarize requires --out")
	}
	if *format != "text" && *format != "json" {
		return fmt.Errorf("unsupported summary format")
	}
	summary, err := summarizeReports(*root)
	if err != nil {
		return err
	}
	if *format == "json" {
		return writeSummaryJSON(stdout, summary)
	}
	fmt.Fprintln(stdout, "Available scenario reports (not proof that the full suite finished):")
	for _, row := range summary.Scenarios {
		if row.Result == "missing" {
			fmt.Fprintf(stdout, "%s: no final report; not proven complete\n", row.Name)
			continue
		}
		duration := "unknown duration"
		if row.DurationSeconds != nil {
			duration = fmt.Sprintf("%.1fs", *row.DurationSeconds)
		}
		fmt.Fprintf(stdout, "%s: %s (%s)\n", row.Name, row.Result, duration)
		if row.FailureMessage != "" {
			fmt.Fprintf(stdout, "  %s\n", row.FailureMessage)
		}
	}
	fmt.Fprintf(stdout, "Reports found: %d; results: %v\n", len(summary.Scenarios)-summary.Counts["missing"], summary.Counts)
	return nil // Inspection does not replace the execution exit code.
}

func summarizeReports(root string) (reportSummary, error) {
	out := reportSummary{Scenarios: []reportSummaryRow{}, Counts: map[string]int{}}
	workspaces := map[string]bool{}
	info, err := os.Lstat(root)
	if err != nil {
		return out, err
	}
	if !info.IsDir() {
		return out, fmt.Errorf("summary root must be a directory, not a symlink")
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() == "kuttl-test.yaml" {
			workspaces[filepath.Dir(path)] = true
		}
		if entry.Name() == "scenario-run-report.json" && filepath.Base(filepath.Dir(path)) == "reports" {
			workspaces[filepath.Dir(filepath.Dir(path))] = true
		}
		if len(workspaces) > 10000 {
			return fmt.Errorf("too many workspaces for summary")
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	paths := make([]string, 0, len(workspaces))
	for path := range workspaces {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		name, _ := filepath.Rel(root, path)
		if name == "." {
			name = filepath.Base(path)
		}
		row := reportSummaryRow{Name: name, Result: "missing"}
		data, err := readRegularScenarioReportSummary(filepath.Join(path, "reports", "scenario-run-report.json"))
		if !os.IsNotExist(err) {
			var report ScenarioRunReport
			if err == nil {
				err = json.Unmarshal(data, &report)
			}
			if err == nil && (report.APIVersion != "spex.report.v0.1" || report.Kind != "ScenarioRunReport" || report.Metadata.Name == "" || report.Status.Result == "") {
				err = fmt.Errorf("invalid scenario report")
			}
			if err == nil {
				contextData, contextErr := readRegularScenarioReportSummary(filepath.Join(path, "scenario-context.json"))
				if contextErr == nil {
					var identity struct {
						Scenario string `json:"scenario"`
						RunID    string `json:"runId"`
					}
					// Reports use the generated DNS label, while context retains
					// the original name. Apply the generator's normalization here.
					if json.Unmarshal(contextData, &identity) != nil || identity.Scenario == "" || workspace.DNSLabel(identity.Scenario) != report.Metadata.Name ||
						(identity.RunID != "" && (report.Metadata.RunID == nil || *report.Metadata.RunID != identity.RunID)) {
						err = fmt.Errorf("report does not match generated scenario identity")
					}
				} else if !os.IsNotExist(contextErr) {
					err = contextErr
				}
			}
			if err != nil {
				row.Result = "error"
				row.FailureMessage = "Unreadable or invalid scenario report"
			} else {
				row.Name = report.Metadata.Name
				row.Result = report.Status.Result
				if report.Status.FailureMessage != nil {
					row.FailureMessage = *report.Status.FailureMessage
				}
				start, e1 := time.Parse(time.RFC3339Nano, report.Status.StartedAt)
				end, e2 := time.Parse(time.RFC3339Nano, report.Status.FinishedAt)
				if e1 == nil && e2 == nil && !end.Before(start) {
					elapsed := end.Sub(start).Seconds()
					row.DurationSeconds = &elapsed
				}
			}
		}
		out.Scenarios = append(out.Scenarios, row)
		out.Counts[row.Result]++
	}
	out.Complete = len(paths) > 0 && out.Counts["missing"] == 0 && out.Counts["error"] == 0
	return out, nil
}

func writeSummaryJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

type groupSummaryEntry struct {
	Scenarios int    `json:"scenarios"`
	Selected  bool   `json:"selected"`
	Status    string `json:"status"`
}

func runGroupSummary(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("reports groups", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", "", "directory containing group-plan.json and <group>-result.json")
	format := fs.String("format", "markdown", "markdown or json")
	title := fs.String("title", "Scenario groups", "Markdown heading")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "reports groups"); err != nil {
		return err
	}
	if *root == "" {
		return fmt.Errorf("reports groups requires --root")
	}
	if *format != "markdown" && *format != "json" {
		return fmt.Errorf("unsupported group summary format")
	}
	data, err := readRegularScenarioReportSummary(filepath.Join(*root, "group-plan.json"))
	if err != nil {
		return err
	}
	var plan map[string]groupSummaryEntry
	if err := json.Unmarshal(data, &plan); err != nil {
		return fmt.Errorf("invalid group plan: %w", err)
	}
	if len(plan) == 0 {
		return fmt.Errorf("empty group plan")
	}
	keys := make([]string, 0, len(plan))
	failed := false
	for name, entry := range plan {
		if name == "" || strings.ContainsAny(name, "/\\\r\n") || name == "." || name == ".." || entry.Scenarios < 0 {
			return fmt.Errorf("invalid group name or scenario count")
		}
		keys = append(keys, name)
		if entry.Selected {
			entry.Status = "not run (blocked or cancelled)"
			data, err := readRegularScenarioReportSummary(filepath.Join(*root, name+"-result.json"))
			if err == nil {
				var result struct {
					ExitCode *int `json:"exitCode"`
				}
				if err := json.Unmarshal(data, &result); err != nil || result.ExitCode == nil {
					return fmt.Errorf("invalid group result for %s", name)
				}
				switch *result.ExitCode {
				case 0:
					entry.Status = "passed"
				case 124:
					entry.Status = "timed out"
				default:
					entry.Status = "failed"
				}
			} else if !os.IsNotExist(err) {
				return err
			}
			failed = failed || entry.Status != "passed"
		} else if entry.Scenarios == 0 {
			entry.Status = "unimplemented"
		} else {
			entry.Status = "not selected"
		}
		plan[name] = entry
	}
	sort.Strings(keys)
	if *format == "json" {
		if err := writeSummaryJSON(stdout, plan); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(stdout, "## %s\n\n| Group | Scenarios | Result |\n| --- | ---: | --- |\n", markdownCell(*title))
		for _, name := range keys {
			entry := plan[name]
			fmt.Fprintf(stdout, "| %s | %d | %s |\n", markdownCell(name), entry.Scenarios, markdownCell(entry.Status))
		}
	}
	if failed {
		return ExitError{Code: 1, Err: fmt.Errorf("selected groups did not all pass")}
	}
	return nil
}

func markdownCell(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ", "\r", " ").Replace(s)
}
