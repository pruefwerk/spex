package main

import (
	"fmt"
	"os"

	"github.com/pruefwerk/spex/internal/spex"
)

func main() {
	if err := spex.Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(spex.ExitCode(err))
	}
}
