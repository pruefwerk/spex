package spex

import (
	"bytes"
	"testing"
)

func TestRuntimeSupportCLIRejectsInvalidAuthoring(t *testing.T) {
	for _, args := range [][]string{{"runtime-support"}, {"runtime-support", "install"}, {"runtime-support", "console"}, {"runtime-support", "helm"}, {"runtime-support", "helm", "--namespace", "production", "--", "k", "release", "chart"}} {
		var output bytes.Buffer
		if err := Run(args, &output, &output); err == nil {
			t.Fatal("invalid authoring accepted", args)
		}
	}
}
