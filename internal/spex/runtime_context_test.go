package spex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pruefwerk/spex/internal/workspace"
	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

func TestRuntimeKUTTLCancellation(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "runner")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	result := executeKUTTLContext(ctx, command, dir, "", io.Discard, io.Discard)
	if !errors.Is(result.Err, context.DeadlineExceeded) || result.FailureClass == nil || *result.FailureClass != "cancelled" {
		t.Fatalf("unexpected cancellation result: %+v", result)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancellation did not stop the command promptly")
	}
}

type runtimeTestSink string

func (s runtimeTestSink) Directory() string { return string(s) }
func (s runtimeTestSink) Write(name string, data []byte) error {
	return os.WriteFile(filepath.Join(string(s), name), data, 0o600)
}

func TestMigrationRuntimeEmptyOverlayParity(t *testing.T) {
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()
	flags, err := parseSuiteFlags("run", []string{"--suite", filepath.Join(root, "examples/suites/mqtt-local.yaml"), "--run-id", "parity", "--command", writeFakeKubectl(t, 0, "")})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := workspace.LoadScenarioSuite(flags.suitePath)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := loadSuiteInputs(resolved, flags)
	if err != nil {
		t.Fatal(err)
	}
	registry := scenarioruntime.NewRegistry()
	if err := registry.Register(migrationRuntime{flags: flags}); err != nil {
		t.Fatal(err)
	}
	prepared, err := registry.Prepare(context.Background(), scenarioruntime.ResolveRequest{
		Workspace: root, Scenario: scenario.Scenario{Schema: scenario.Schema, Runtime: "migration-testbench/v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapted := prepared.Runtime.(*preparedMigrationRuntime)
	if !reflect.DeepEqual(legacy, adapted.inputs) || !reflect.DeepEqual(resolved, adapted.resolved) {
		t.Fatal("empty overlay changed inherited configuration")
	}
	if len(prepared.Plan.Summary().Tests) != 5 {
		t.Fatal("runtime changed test discovery")
	}
	result, err := prepared.Execute(context.Background(), runtimeTestSink(t.TempDir()))
	if err != nil || result.Outcome != scenarioruntime.Passed || len(result.Tests) != 5 {
		t.Fatalf("unexpected runtime result: %+v, %v", result, err)
	}
}

func TestMigrationRuntimeClassifiesEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status ReportStatus
		steps  []ReportStep
		want   scenarioruntime.Outcome
	}{
		{"passed", ReportStatus{ScenarioResult: "passed", RunnerResult: "passed"}, nil, scenarioruntime.Passed},
		{"setup failed", ReportStatus{ScenarioResult: "failed", RunnerResult: "passed"}, []ReportStep{{Result: "failed", Internal: true}}, scenarioruntime.Error},
		{"assertion failed", ReportStatus{ScenarioResult: "failed", RunnerResult: "passed"}, []ReportStep{{Result: "failed"}}, scenarioruntime.Failed},
		{"unmapped failure", ReportStatus{ScenarioResult: "failed", RunnerResult: "passed"}, nil, scenarioruntime.Error},
		{"runner failed", ReportStatus{ScenarioResult: "not_run", RunnerResult: "error"}, nil, scenarioruntime.Error},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "test", "reports")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(ScenarioRunReport{Metadata: ReportMetadata{Name: "test"}, Status: tc.status, Steps: tc.steps})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "scenario-run-report.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			result := migrationExecutionEvidence(root, 1, false)
			if result.Outcome != tc.want {
				t.Fatalf("want %s, got %+v", tc.want, result)
			}
		})
	}
	result := migrationExecutionEvidence(t.TempDir(), 1, false)
	if result.Outcome != scenarioruntime.Error || result.Cleanup != "incomplete" {
		t.Fatalf("missing evidence treated as success: %+v", result)
	}
}

func TestRuntimeRateLimitCancellation(t *testing.T) {
	limiter := suiteRateLimiter{interval: time.Hour, next: time.Now().Add(time.Hour)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(limiter.WaitContext(ctx), context.Canceled) {
		t.Fatal("rate limiter ignored cancellation")
	}
}

func TestRuntimeCancelledSuiteDoesNotMutateWorkspace(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runResolvedSuite(workspace.ResolvedScenarioSuite{}, nil, suiteFlags{ctx: ctx, outPath: dir}, io.Discard, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("cancelled execution changed the output directory")
	}
}

func TestRuntimeSharedSuiteExecutionParity(t *testing.T) {
	root := repoRoot(t)
	restore := chdir(t, root)
	defer restore()
	command := writeFakeKubectl(t, 0, "")
	args := []string{"--suite", "examples/suites/mqtt-local.yaml", "--out", filepath.Join(t.TempDir(), "suite"), "--run-id", "parity", "--command", command}
	var legacy, shared bytes.Buffer
	if err := runSuiteRun(args, &legacy, io.Discard); err != nil {
		t.Fatal(err)
	}
	flags, err := parseSuiteFlags("run", args)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(flags.outPath, "reports", "suite-junit.xml"))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := workspace.LoadScenarioSuite(flags.suitePath)
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := loadSuiteInputs(resolved, flags)
	if err != nil {
		t.Fatal(err)
	}
	if err := runResolvedSuite(resolved, inputs, flags, &shared, io.Discard); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(flags.outPath, "reports", "suite-junit.xml"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("shared execution changed JUnit evidence: %v", err)
	}
}
