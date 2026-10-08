package engine

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func (h *Host) kindCommand(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("Kind operation required")
	}
	flags := flag.NewFlagSet("kind", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", ".", "runtime checkout")
	scope := flags.String("scope", os.Getenv(h.config.ScopeEnvironment), "execution scope")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *scope == "" {
		return errors.New("invalid Kind operation arguments")
	}
	switch args[0] {
	case "configure":
		path, err := h.configureKind(*root, *scope)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, path)
		return err
	case "images":
		return h.prepareKindImages(ctx, *root, *scope, nil)
	case "create", "cleanup":
		return h.kindOperation(ctx, *root, *scope, args[0])
	default:
		return errors.New("unknown Kind operation")
	}
}
