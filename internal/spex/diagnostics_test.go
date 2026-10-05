package spex

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestDiagnosticsOnlyRequestsRestartedContainerLogs(t *testing.T) {
	var calls [][]string
	run := func(ctx context.Context, args []string) ([]byte, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing command deadline")
		}
		calls = append(calls, args)
		if strings.Contains(strings.Join(args, " "), "get pods") {
			return []byte(`{"items":[{"metadata":{"name":"pod"},"status":{"containerStatuses":[{"name":"app","restartCount":0},{"name":"sidecar","restartCount":2}],"initContainerStatuses":[{"name":"init","restartCount":1}]}}]}`), nil
		}
		return []byte("logs\n"), nil
	}
	var out bytes.Buffer
	err := runDiagnosticsWith([]string{"previous-logs", "--kubeconfig", "config", "--namespace", "example", "--selector", "app=test", "--tail", "100"}, &out, run)
	if err != nil || len(calls) != 3 {
		t.Fatalf("calls=%v err=%v", calls, err)
	}
	for _, args := range calls[1:] {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--previous --tail=100") || strings.Contains(joined, "-c app") {
			t.Fatalf("unexpected command: %s", joined)
		}
	}
}

func TestDiagnosticsCollectReadOnlyAndContinueOnFailure(t *testing.T) {
	var calls int
	run := func(ctx context.Context, args []string) ([]byte, error) {
		calls++
		for _, arg := range args {
			if arg == "delete" || arg == "apply" {
				t.Fatal("mutation")
			}
		}
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "get pods -l") {
			return []byte(`{"items":[]}`), nil
		}
		if strings.Contains(joined, "describe pods") {
			return nil, fmt.Errorf("gone")
		}
		return []byte("diagnostics\n"), nil
	}
	var out bytes.Buffer
	err := runDiagnosticsWith([]string{"collect", "--kubeconfig", "config", "--namespace", "example", "--selector", "spex/owned=true"}, &out, run)
	if err == nil || calls != 5 || !strings.Contains(out.String(), "skipping previous logs") {
		t.Fatalf("calls=%d err=%v output=%s", calls, err, out.String())
	}
}

func TestDiagnosticsRejectsMissingScopeAndUnboundedOptions(t *testing.T) {
	for _, args := range [][]string{{"collect"}, {"previous-logs", "--kubeconfig", "config", "--namespace", "example", "--selector", "app=test", "--tail", "-1"}, {"collect", "--kubeconfig", "config", "--namespace", "example", "--selector", "app=test", "--timeout", "11m"}} {
		if err := runDiagnosticsWith(args, &bytes.Buffer{}, func(context.Context, []string) ([]byte, error) {
			t.Fatal("called kubectl with invalid options")
			return nil, nil
		}); err == nil {
			t.Fatal("accepted invalid diagnostics")
		}
	}
}

func TestDiagnosticOutputCap(t *testing.T) {
	var buffer cappedDiagnosticBuffer
	input := bytes.Repeat([]byte("x"), 2<<20)
	n, err := buffer.Write(input)
	if err != nil || n != len(input) || buffer.data.Len() != 1<<20 || !buffer.truncated {
		t.Fatal("diagnostic output not bounded")
	}
}
