package main

import (
	"fmt"
	"os"

	"github.com/pruefwerk/spex/internal/probe"
	"github.com/pruefwerk/spex/internal/spex"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		if err := spex.Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(spex.ExitCode(err))
		}
		return
	}
	if err := probe.RunProvider("influxdb", os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
