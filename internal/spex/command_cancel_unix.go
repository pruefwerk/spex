//go:build unix

package spex

import (
	"os"
	"os/exec"
	"syscall"
)

// Isolate cancellable commands so their children cannot outlive cancellation.
func cancelCommandTree(cmd *exec.Cmd) {
	if cmd.Cancel == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
}
