package runtimehelpers

import (
	"context"
	"errors"
	"flag"
	"io"
)

// RunCLI keeps portable helper argument semantics in one place. Executable
// adapters own signals and exit reporting, not Helm reuse or console behavior.
func RunCLI(ctx context.Context, args []string, stdout io.Writer) (int, error) {
	if len(args) == 0 {
		return 2, errors.New("runtime-support requires console or helm")
	}
	switch args[0] {
	case "console":
		if len(args) < 3 {
			return 2, errors.New("console requires log path and command")
		}
		command := args[2:]
		if command[0] == "--" {
			command = command[1:]
		}
		code, err := CaptureConsole(ctx, command, args[1], stdout)
		if err == nil && code != 0 {
			err = errors.New("captured command failed; inspect console artifact")
		}
		return code, err
	case "helm":
		flags := flag.NewFlagSet("runtime-support helm", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		namespace := flags.String("namespace", "", "runtime namespace")
		prefix := flags.String("receipt-prefix", "", "receipt prefix")
		manager := flags.String("manager", "", "receipt manager")
		timeout := flags.String("timeout", "", "installation timeout")
		if flags.Parse(args[1:]) != nil || flags.NArg() < 3 {
			return 2, errors.New("helm requires policy flags and kubeconfig, release, chart")
		}
		values := flags.Args()
		err := EnsureHelm(ctx, values[0], values[1], values[2], values[3:], HelmPolicy{Namespace: *namespace, ReceiptPrefix: *prefix, Manager: *manager, Timeout: *timeout}, nil, stdout)
		if err != nil {
			return 1, err
		}
		return 0, nil
	default:
		return 2, errors.New("unknown runtime-support command")
	}
}
