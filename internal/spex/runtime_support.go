package spex

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/pruefwerk/spex/pkg/runtimehelpers"
)

func runRuntimeSupport(args []string, stdout io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code, err := runtimehelpers.RunCLI(ctx, args, stdout)
	if err != nil {
		return ExitError{Code: code, Err: err}
	}
	return nil
}
