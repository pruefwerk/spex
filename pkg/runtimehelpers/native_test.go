package runtimehelpers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type helmFixture struct {
	state    string
	revision int
	values   map[string]any
	receipt  []byte
	installs int
	race     bool
	chart    string
}

func (f *helmFixture) run(_ context.Context, args []string, body []byte) ([]byte, error) {
	var value any
	switch {
	case args[0] == "helm" && args[1] == "list":
		if f.state == "absent" {
			value = []any{}
		} else {
			value = []any{map[string]string{"status": f.state}}
		}
	case args[0] == "helm" && args[1] == "upgrade":
		f.installs++
		f.state = "deployed"
		return nil, nil
	case args[0] == "helm" && args[1] == "status":
		value = map[string]any{"version": f.revision, "info": map[string]string{"status": f.state}}
	case args[0] == "helm" && args[1] == "get" && contains(args, "metadata"):
		value = map[string]any{"name": "fixture", "namespace": "test-runtime", "revision": f.revision, "status": f.state, "chart": f.chart, "version": "1.0.0"}
	case args[0] == "helm" && args[1] == "get" && contains(args, "values"):
		value = f.values
		if f.race {
			f.revision++
		}
	case args[0] == "kubectl" && contains(args, "get"):
		return f.receipt, nil
	case args[0] == "kubectl" && contains(args, "apply"):
		f.receipt = append([]byte{}, body...)
		return nil, nil
	default:
		return nil, errors.New("unexpected command")
	}
	return json.Marshal(value)
}
func contains(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func TestReceiptHashMatchesPreviousEncoding(t *testing.T) {
	value := struct {
		Chart     string            `json:"chart"`
		Arguments []string          `json:"arguments"`
		Files     map[string]string `json:"files"`
	}{"example", []string{"café<>&", "🚀"}, map[string]string{}}
	got, err := digest(value)
	if err != nil || got != "7a71341d4c6837139a67513e891c9286b5912416f251efff3330b4ad8a14f183" {
		t.Fatal(got, err)
	}
}

func TestLocalChartFingerprintTracksContentAndRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	chart := filepath.Join(dir, "chart")
	if err := os.Mkdir(chart, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chart, "Chart.yaml"), []byte("name: fixture\nversion: 1.0.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(chart, "values.yaml")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := RequestFingerprint(chart, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := RequestFingerprint(chart, nil)
	if err != nil || before == after {
		t.Fatal(after, err)
	}
	if err := os.Symlink(path, filepath.Join(chart, "link")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := RequestFingerprint(chart, nil); err == nil {
		t.Fatal("chart symlink accepted")
	}
}
func helmTest(t *testing.T) (*helmFixture, func([]string) error, *bytes.Buffer) {
	t.Helper()
	f := &helmFixture{state: "absent", revision: 1, values: map[string]any{"token": "distinctive-unit-secret"}, chart: "fixture"}
	var output bytes.Buffer
	return f, func(args []string) error {
		return EnsureHelm(context.Background(), "config", "fixture", "oci://example/fixture", args, HelmPolicy{"test-runtime", "mtb-shared", "testbench", "8m"}, f.run, &output)
	}, &output
}

func TestNativeHelmInstallReuseAndSecretSafety(t *testing.T) {
	f, ensure, output := helmTest(t)
	if err := ensure(nil); err != nil {
		t.Fatal(err)
	}
	if err := ensure(nil); err != nil || f.installs != 1 {
		t.Fatal(err, f.installs)
	}
	if strings.Contains(string(f.receipt)+output.String(), "distinctive-unit-secret") {
		t.Fatal("secret persisted")
	}
	if !strings.Contains(output.String(), "Reusing checked") {
		t.Fatal(output.String())
	}
}
func TestNativeHelmRejectsRequestedAndObservedDrift(t *testing.T) {
	for _, change := range []string{"arguments", "revision", "values", "metadata"} {
		t.Run(change, func(t *testing.T) {
			f, ensure, _ := helmTest(t)
			if err := ensure(nil); err != nil {
				t.Fatal(err)
			}
			var args []string
			switch change {
			case "arguments":
				args = []string{"--version", "2.0.0"}
			case "revision":
				f.revision++
			case "values":
				f.values["extra"] = true
			case "metadata":
				f.chart = "changed"
			}
			if err := ensure(args); err == nil || f.installs != 1 {
				t.Fatal("drift hidden", err, f.installs)
			}
		})
	}
}
func TestNativeHelmFailsClosedWithoutReceipt(t *testing.T) {
	for _, state := range []string{"deployed", "failed", "pending-upgrade"} {
		f, ensure, _ := helmTest(t)
		f.state = state
		if err := ensure(nil); err == nil || f.installs != 0 {
			t.Fatal(state, err, f.installs)
		}
	}
}
func TestNativeHelmRacingRevisionCannotWriteReceipt(t *testing.T) {
	f, ensure, _ := helmTest(t)
	f.race = true
	if err := ensure(nil); err == nil || f.receipt != nil {
		t.Fatal("race accepted", err)
	}
}
func TestNativeHelmTargetOverrideStopsBeforeTools(t *testing.T) {
	for _, flag := range []string{"--namespace=production", "--kubeconfig=other", "--kube-context=other"} {
		calls := 0
		err := EnsureHelm(context.Background(), "config", "fixture", "chart", []string{flag}, HelmPolicy{"test-runtime", "mtb-shared", "testbench", "8m"}, func(context.Context, []string, []byte) ([]byte, error) { calls++; return nil, nil }, nil)
		if err == nil || calls != 0 {
			t.Fatal(flag, err, calls)
		}
	}
}
func TestNativeHelmFingerprintTracksInputs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "values.yaml")
	if err := os.WriteFile(path, []byte("replicas: 1"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := RequestFingerprint("oci://chart", []string{"--values", path})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replicas: 2"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := RequestFingerprint("oci://chart", []string{"--values", path})
	if err != nil || before == after {
		t.Fatal(err)
	}
}

func TestConsoleChild(t *testing.T) {
	if os.Getenv("SPEX_CONSOLE_CHILD") != "1" {
		return
	}
	if os.Getenv("SPEX_CONSOLE_WAIT") == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if code := os.Getenv("SPEX_CONSOLE_STATUS"); code != "" {
		value, _ := strconv.Atoi(code)
		os.Exit(value)
	}
	for i := 0; i < 200; i++ {
		fmt.Printf("detail-%d\n", i)
	}
	fmt.Println("Starting scenario 1/1: fixture")
	fmt.Println("Finished scenario 1/1: fixture")
	if os.Getenv("SPEX_CONSOLE_FAIL") == "1" {
		fmt.Fprintln(os.Stderr, "failed assertion")
		os.Exit(7)
	}
	os.Exit(0)
}

func TestNativeConsolePreservesTimeoutAndCancellation(t *testing.T) {
	t.Setenv("SPEX_CONSOLE_CHILD", "1")
	t.Setenv("SPEX_CONSOLE_STATUS", "124")
	var output bytes.Buffer
	code, err := CaptureConsole(context.Background(), []string{os.Args[0], "-test.run=^TestConsoleChild$"}, filepath.Join(t.TempDir(), "timeout.log"), &output)
	if err != nil || code != 124 {
		t.Fatal(code, err)
	}
	t.Setenv("SPEX_CONSOLE_STATUS", "")
	t.Setenv("SPEX_CONSOLE_WAIT", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	code, err = CaptureConsole(ctx, []string{os.Args[0], "-test.run=^TestConsoleChild$"}, filepath.Join(t.TempDir(), "cancelled.log"), &output)
	if err != nil || code != 130 {
		t.Fatal(code, err)
	}
}

func TestNativeToolFailuresDoNotEchoArgumentsOrOutput(t *testing.T) {
	t.Setenv("SPEX_CONSOLE_CHILD", "1")
	t.Setenv("SPEX_CONSOLE_FAIL", "1")
	output, err := RunTool(context.Background(), []string{os.Args[0], "-test.run=^TestConsoleChild$"}, []byte("distinctive-unit-secret"))
	if err == nil || len(output) != 0 || strings.Contains(err.Error(), "distinctive-unit-secret") || strings.Contains(err.Error(), "failed assertion") {
		t.Fatal("tool failure leaked data", err)
	}
}
func TestNativeConsoleCapture(t *testing.T) {
	for _, fail := range []string{"0", "1"} {
		t.Run(fail, func(t *testing.T) {
			t.Setenv("SPEX_CONSOLE_CHILD", "1")
			t.Setenv("SPEX_CONSOLE_FAIL", fail)
			var output bytes.Buffer
			path := filepath.Join(t.TempDir(), "console.log")
			code, err := CaptureConsole(context.Background(), []string{os.Args[0], "-test.run=^TestConsoleChild$"}, path, &output)
			if err != nil {
				t.Fatal(err)
			}
			expected := 0
			if fail == "1" {
				expected = 7
			}
			if code != expected {
				t.Fatal(code)
			}
			data, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(data), "detail-0\n") {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "detail-0\n") {
				t.Fatal("full setup streamed")
			}
			if fail == "1" && (!strings.Contains(output.String(), "failed assertion") || len(strings.Split(output.String(), "\n")) > 65) {
				t.Fatal(output.String())
			}
		})
	}
}
