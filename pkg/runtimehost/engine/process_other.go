//go:build !linux && !darwin

package engine

import "os/exec"

func configureCancellation(cmd *exec.Cmd) {}
