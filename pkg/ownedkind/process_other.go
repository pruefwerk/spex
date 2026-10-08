//go:build !unix

package ownedkind

import "os/exec"

func configureCommand(command *exec.Cmd) {}
