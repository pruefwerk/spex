package ownedkind

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

type CommandRunner struct{ Directory string }

type boundedOutput struct {
	bytes.Buffer
	exceeded bool
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	n := len(data)
	remaining := (4 << 20) - b.Len()
	if len(data) > remaining {
		b.exceeded = true
		data = data[:remaining]
	}
	_, _ = b.Buffer.Write(data)
	return n, nil
}

func (r CommandRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("resource command required")
	}
	limit := 15 * time.Minute
	if len(args) > 1 && args[0] == "docker" && args[1] == "rm" {
		limit = 2 * time.Minute
	}
	child, stop := context.WithTimeout(ctx, limit)
	defer stop()
	command := exec.CommandContext(child, args[0], args[1:]...)
	command.Dir = r.Directory
	configureCommand(command)
	var stdout, stderr boundedOutput
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if child.Err() != nil {
		return "", child.Err()
	}
	if err != nil {
		return "", errors.New("resource command failed")
	}
	if stdout.exceeded {
		return "", errors.New("resource command output exceeds limit")
	}
	return stdout.String(), nil
}
