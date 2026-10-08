package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pruefwerk/spex/pkg/receiver"
	"github.com/pruefwerk/spex/pkg/runtimehelpers"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
)

var hostProcess sync.Mutex

// admissionContext contains authenticated identities and host-owned paths, never
// credentials. The fetcher creates it privately in this execution's checkout.
type admissionContext struct {
	Schema     string            `json:"schema"`
	Expected   receiver.Expected `json:"expected"`
	Checkout   receiver.Checkout `json:"checkout"`
	Artifact   string            `json:"artifact"`
	Root       string            `json:"root"`
	Suite      string            `json:"suite"`
	Scope      string            `json:"scope"`
	Kubeconfig string            `json:"kubeconfig"`
	Actor      string            `json:"actor"`
}

func readJSON(path string, into any) error {
	file, err := os.Open(path)
	if err != nil {
		return errors.New("private context unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return errors.New("invalid private context")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(into) != nil {
		return errors.New("invalid private context")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("invalid private context")
	}
	return nil
}

func (h *Host) run(ctx context.Context, contextPath string, stdout, stderr io.Writer) int {
	var admitted admissionContext
	if readJSON(contextPath, &admitted) != nil || admitted.Schema != h.resolver.Protocol("receiver-admission") {
		fmt.Fprintln(stderr, "Receiver admission context invalid")
		return 1
	}
	root, err := filepath.Abs(".")
	if err != nil || admitted.Root != root {
		return 1
	}
	// Fixed host paths prevent a context from selecting arbitrary setup code.
	private := filepath.Join(root, ".spex", "receiver-private")
	if contextPath != filepath.Join(private, "context.json") || admitted.Artifact != filepath.Join(private, "request.zip") || admitted.Checkout.Directory != filepath.Join(private, "source") {
		return 1
	}
	if err := os.Setenv(h.config.ScopeEnvironment, admitted.Scope); err != nil {
		return 1
	}
	if err := os.Setenv("KUBECONFIG", admitted.Kubeconfig); err != nil {
		return 1
	}
	if admitted.Actor == "" {
		return 1
	}
	runtime, err := h.newKindRuntime(admitted, "git-"+os.Getenv("GITHUB_SHA"))
	if err != nil {
		return 1
	}
	archive, err := os.Open(admitted.Artifact)
	if err != nil {
		return 1
	}
	defer archive.Close()
	data, err := io.ReadAll(io.LimitReader(archive, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return 1
	}
	request, err := receiver.DecodeArtifact(data, admitted.Expected)
	if err != nil {
		fmt.Fprintln(stderr, "Receiver request invalid")
		return 1
	}
	sink, err := scenarioruntime.NewFileArtifacts(root, ".spex/receiver-evidence", request.SHA256())
	if err != nil {
		return 1
	}
	defer sink.Close()
	outputs := func(name, value string) {
		path := os.Getenv("GITHUB_OUTPUT")
		if path == "" || strings.ContainsAny(value, "\r\n") {
			return
		}
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err == nil {
			defer file.Close()
			fmt.Fprintf(file, "%s=%s\n", name, value)
		}
	}
	outputs("evidence-directory", sink.Directory())
	host, err := h.makeHost(admitted, runtime)
	if err != nil {
		return 1
	}
	admission, closeStore, err := h.openScheduling(ctx, root, os.Getenv("GITHUB_REPOSITORY")+"/"+os.Getenv("GITHUB_RUN_ID")+"/"+os.Getenv("GITHUB_RUN_ATTEMPT"))
	if err != nil {
		fmt.Fprintln(stderr, "Receiver scheduling unavailable; no test environment started")
		return 1
	}
	defer closeStore()
	host.Scheduling = admission
	mode := "enabled"
	if admission == nil {
		mode = "disabled"
	}
	modeData, _ := json.Marshal(struct {
		Schema string `json:"schema"`
		Mode   string `json:"mode"`
	}{h.resolver.Protocol("scheduling-mode"), mode})
	if sink.Write("scheduling-mode.json", modeData) != nil {
		fmt.Fprintln(stderr, "Cannot persist scheduling mode; no test environment started")
		return 1
	}
	fmt.Fprintf(stdout, "Capacity scheduling: %s\n", mode)
	receipt, primary := receiver.Execute(ctx, request, admitted.Checkout, host, sink)
	if _, err := os.Stat(filepath.Join(sink.Directory(), "receipt.json")); err == nil {
		outputs("receipt-path", filepath.Join(sink.Directory(), "receipt.json"))
	}
	fmt.Fprintf(stdout, "Receiver outcome: %s; planned tests: %d; cleanup: %s\n", receipt.Result.Outcome, receipt.PlannedTests, receipt.Result.Cleanup)
	if primary != nil {
		fmt.Fprintln(stderr, "Receiver execution did not complete successfully; inspect receipt and evidence")
	}
	return receiver.ExitCode(receipt, primary)
}

// Run loads a host snapshot and executes one operation. Use separate processes
// for concurrent operations that need the inherited resolver's environment.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(stdout, "Usage: spex runtime --config <host.toml> <operation>\nOperations: validate, execution, kind, config, ci, source-admit, receiver-cleanup, credentials, helpers\nRun execution commands from the trusted runtime checkout.")
		return 0
	}
	h, err := New(pathFromArgs(args), version)
	if err != nil {
		fmt.Fprintln(stderr, "Runtime host definition invalid; no environment changes performed")
		return 2
	}
	if len(args) >= 2 && args[0] == "--config" {
		args = args[2:]
	}
	return h.Run(ctx, args, stdout, stderr)
}

// Run serializes only the inherited resolver's environment boundary. Policy
// and version belong to this host, not to the shared process.
func (h *Host) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if !hostProcess.TryLock() {
		fmt.Fprintln(stderr, "Run environment-dependent host operations in separate processes")
		return 2
	}
	defer hostProcess.Unlock()
	return h.runOperation(ctx, args, stdout, stderr)
}

func (h *Host) runOperation(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	restore := h.captureEnvironment()
	defer restore()
	if os.Setenv(hostconfig.ConfigEnvironment, h.path) != nil {
		return 2
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Runtime operation required")
		return 2
	}
	fail := func(message string, code int, err error) int {
		if err != nil {
			fmt.Fprintln(stderr, message)
			return code
		}
		return 0
	}
	switch args[0] {
	case "validate":
		if len(args) != 1 {
			return 2
		}
		fmt.Fprintln(stdout, "Runtime host definition valid; no environment changes performed")
		return 0
	case "source-admit":
		if len(args) != 1 {
			return 2
		}
		if h.admitSource(ctx, ".", os.Getenv, receiver.GitHubSource{Token: os.Getenv("SOURCE_READ_TOKEN")}) != nil {
			fmt.Fprintln(stderr, "Receiver source admission failed; execution did not start")
			return 1
		}
		fmt.Fprintln(stdout, "Receiver source admitted; no test environment created")
		return 0
	case "credentials":
		if len(args) != 1 {
			return 2
		}
		return fail("AWS scenario credential renewal failed", 1, h.renewCredentials(ctx, stdout))
	case "ci":
		return fail("CI planning, validation or reporting failed", 1, h.ciCommand(ctx, args[1:], stdout))
	case "helpers", "gateway":
		return fail("Configured helper failed; check input or readiness", 1, h.helperCommand(ctx, args[1:], stdout))
	case "receiver-cleanup":
		if len(args) != 1 {
			return 2
		}
		return fail("Receiver cleanup incomplete; inspect ownership evidence", 1, h.cleanupReceiver("."))
	case "runtime-support":
		code, err := runtimehelpers.RunCLI(ctx, args[1:], stdout)
		if err != nil {
			fmt.Fprintln(stderr, err)
		}
		return code
	case "execution":
		err := h.executionCommand(ctx, args[1:], stdout)
		code := 1
		if ctx.Err() != nil {
			code = 130
		}
		return fail("Execution gate did not complete; inspect ownership and scheduling evidence", code, err)
	case "kind":
		return fail("Kind operation did not complete; inspect execution ledgers", 1, h.kindCommand(ctx, args[1:], stdout))
	case "config":
		return fail("Runtime configuration invalid; no environment changes performed", 2, h.configuration(args[1:], stdout))
	case "capacity-acquire", "capacity-check", "capacity-finish":
		if len(args) != 2 {
			return 2
		}
		return h.capacityCommand(ctx, args[0], args[1])
	default:
		if len(args) != 1 {
			return 2
		}
		return h.run(ctx, args[0], stdout, stderr)
	}
}
