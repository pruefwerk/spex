package ownedkind

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestCommandRunnerHelper(t *testing.T) {
	if os.Getenv("SPEX_OWNED_KIND_HELPER") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, "PRIVATE_TOOL_OUTPUT")
	fmt.Fprintln(os.Stderr, "PRIVATE_TOOL_ERROR")
	os.Exit(7)
}

func TestCommandRunnerHidesToolFailuresAndPreservesCancellation(t *testing.T) {
	t.Setenv("SPEX_OWNED_KIND_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, err = (CommandRunner{}).Run(context.Background(), executable, "-test.run=^TestCommandRunnerHelper$")
	if err == nil || strings.Contains(err.Error(), "PRIVATE_TOOL") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (CommandRunner{}).Run(ctx, executable, "-test.run=^TestCommandRunnerHelper$")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCommandOutputIsBoundedWithoutBlockingWriters(t *testing.T) {
	var output boundedOutput
	data := make([]byte, 5<<20)
	n, err := output.Write(data)
	if err != nil || n != len(data) || output.Len() != 4<<20 || !output.exceeded {
		t.Fatal(n, err)
	}
}
