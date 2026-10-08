// Package command composes executable commands without making runtime services
// depend on the executable's dispatch layer.
package command

import (
	"context"
	"fmt"
	"github.com/pruefwerk/spex/internal/spex"
	"github.com/pruefwerk/spex/pkg/runtimehost/engine"
	"io"
)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "runtime" {
		return engine.Run(ctx, args[1:], stdout, stderr, spex.Version)
	}
	if err := spex.Run(args, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, err)
		return spex.ExitCode(err)
	}
	return 0
}
