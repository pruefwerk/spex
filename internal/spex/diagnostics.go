package spex

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"time"
)

// Keep diagnostic collection read-only and bounded. Callers choose the namespace
// and labels; Spex never assumes which application services should exist.
type diagnosticRunner func(context.Context, []string) ([]byte, error)

func runDiagnostics(args []string, stdout io.Writer) error {
	return runDiagnosticsWith(args, stdout, kubectlDiagnostic)
}

func kubectlDiagnostic(ctx context.Context, args []string) ([]byte, error) {
	command := exec.CommandContext(ctx, "kubectl", args...)
	var buffer cappedDiagnosticBuffer
	command.Stdout = &buffer
	command.Stderr = &buffer
	err := command.Run()
	if buffer.truncated {
		buffer.data.WriteString("\n[diagnostic output truncated]\n")
	}
	return buffer.data.Bytes(), err
}

type cappedDiagnosticBuffer struct {
	data      bytes.Buffer
	truncated bool
}

func (b *cappedDiagnosticBuffer) Write(p []byte) (int, error) {
	n := len(p)
	available := (1 << 20) - b.data.Len()
	if available < len(p) {
		b.truncated = true
		p = p[:available]
	}
	b.data.Write(p)
	return n, nil
}

type diagnosticPodList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			Containers []diagnosticContainer `json:"containerStatuses"`
			Init       []diagnosticContainer `json:"initContainerStatuses"`
		} `json:"status"`
	} `json:"items"`
}
type diagnosticContainer struct {
	Name     string `json:"name"`
	Restarts int    `json:"restartCount"`
}

func runDiagnosticsWith(args []string, stdout io.Writer, run diagnosticRunner) error {
	if len(args) == 0 || (args[0] != "previous-logs" && args[0] != "collect") {
		return fmt.Errorf("diagnostics requires previous-logs or collect")
	}
	mode := args[0]
	fs := flag.NewFlagSet("diagnostics "+mode, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config := fs.String("kubeconfig", "", "explicit kubeconfig")
	namespace := fs.String("namespace", "", "explicit namespace")
	selector := fs.String("selector", "", "pod label selector")
	tail := fs.Int("tail", 300, "log lines per container")
	timeout := fs.Duration("timeout", 2*time.Minute, "total collection deadline")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "diagnostics"); err != nil {
		return err
	}
	if *config == "" || *namespace == "" || *selector == "" {
		return fmt.Errorf("diagnostics requires --kubeconfig, --namespace and --selector")
	}
	if *tail < 1 || *tail > 10000 || *timeout <= 0 || *timeout > 10*time.Minute {
		return fmt.Errorf("diagnostic tail or timeout outside allowed bounds")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	base := []string{"--kubeconfig", *config, "--request-timeout=15s", "-n", *namespace}
	call := func(arguments ...string) ([]byte, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		child, stop := context.WithTimeout(ctx, 20*time.Second)
		defer stop()
		return run(child, append(append([]string{}, base...), arguments...))
	}
	var failures int
	section := func(title string, arguments ...string) {
		fmt.Fprintf(stdout, "== %s ==\n", title)
		data, err := call(arguments...)
		stdout.Write(data)
		if err != nil {
			failures++
			fmt.Fprintln(stdout, "[diagnostic command failed or timed out]")
		}
	}
	if mode == "collect" {
		section("resources", "get", "pods,jobs,deploy,statefulset", "-l", *selector, "-o", "wide")
		section("events", "get", "events", "--sort-by=.lastTimestamp")
		section("describe pods", "describe", "pods", "-l", *selector)
		section("current logs", "logs", "-l", *selector, "--all-containers", "--prefix=true", "--tail="+strconv.Itoa(*tail))
	}
	data, err := call("get", "pods", "-l", *selector, "-o", "json")
	if err != nil {
		return fmt.Errorf("diagnostics could not list pods")
	}
	var pods diagnosticPodList
	if err := json.Unmarshal(data, &pods); err != nil {
		return fmt.Errorf("diagnostics could not decode pod list")
	}
	count := 0
	for _, pod := range pods.Items {
		for _, container := range append(pod.Status.Init, pod.Status.Containers...) {
			if container.Restarts <= 0 {
				continue
			}
			count++
			section("previous logs: "+pod.Metadata.Name+"/"+container.Name, "logs", pod.Metadata.Name, "-c", container.Name, "--prefix=true", "--previous", "--tail="+strconv.Itoa(*tail))
		}
	}
	if count == 0 {
		fmt.Fprintln(stdout, "No restarted containers; skipping previous logs.")
	}
	if failures > 0 {
		return fmt.Errorf("diagnostic collection incomplete: %d commands failed", failures)
	}
	return nil
}
