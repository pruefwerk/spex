//go:build windows

package runtimehelpers

import "os/exec"

func prepareProcess(cmd *exec.Cmd) {}
func processExitCode(err *exec.ExitError) int {
	code := err.ExitCode()
	if code < 0 {
		return 1
	}
	return code
}
