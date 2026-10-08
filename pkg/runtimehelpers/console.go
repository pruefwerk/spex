package runtimehelpers

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// RunTool captures only bounded tool output. Failures never echo argv, stdin,
// stderr or stdout because Helm inputs may contain expanded credentials.
func RunTool(ctx context.Context, args []string, body []byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("tool required")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = bytes.NewReader(body)
	cmd.WaitDelay = 5 * time.Second
	var buf limitedBuffer
	cmd.Stdout = &buf
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, errors.New("runtime tool failed")
	}
	if buf.overflow {
		return nil, errors.New("runtime tool output exceeds limit")
	}
	return buf.Bytes(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	size := len(p)
	left := (4 << 20) - b.Len()
	if size > left {
		b.overflow = true
		p = p[:left]
	}
	_, _ = b.Buffer.Write(p)
	return size, nil
}

// CaptureConsole writes the complete child stream to a private artifact and
// emits scenario progress plus bounded failure excerpts. Argv never uses a shell.
func CaptureConsole(ctx context.Context, args []string, logPath string, output io.Writer) (int, error) {
	if len(args) == 0 || logPath == "" || output == nil {
		return 1, errors.New("console requires command, log path and output")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		return 1, errors.New("console directory unavailable")
	}
	if info, err := os.Lstat(logPath); err == nil && !info.Mode().IsRegular() {
		return 1, errors.New("console destination must be regular")
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return 1, errors.New("console artifact unavailable")
	}
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return 1, errors.New("console artifact permissions unavailable")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	prepareProcess(cmd)
	cmd.WaitDelay = 5 * time.Second
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return 1, errors.New("console capture unavailable")
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return 1, errors.New("console command unavailable")
	}
	fmt.Fprintln(output, "Full suite output:", logPath)
	errorsTail, tail := []string{}, []string{}
	appendTail := func(lines []string, line string, n int) []string {
		lines = append(lines, line)
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		return lines
	}
	matcher := regexp.MustCompile(`(?i)\b(error|failed|failure|exception|traceback)\b`)
	reader := bufio.NewReader(io.TeeReader(pipe, file))
	var readErr error
	for {
		var snippet []byte
		var err error
		for {
			fragment, more, fragmentErr := reader.ReadLine()
			left := 2000 - len(snippet)
			if len(fragment) > left {
				fragment = fragment[:left]
			}
			snippet = append(snippet, fragment...)
			err = fragmentErr
			if !more || err != nil {
				break
			}
		}
		if len(snippet) > 0 {
			excerpt := string(snippet) + "\n"
			tail = appendTail(tail, excerpt, 40)
			if matcher.MatchString(excerpt) {
				errorsTail = appendTail(errorsTail, excerpt, 20)
			}
			if strings.HasPrefix(excerpt, "Starting scenario ") || strings.HasPrefix(excerpt, "Finished scenario ") || strings.HasPrefix(excerpt, "::error::") {
				fmt.Fprint(output, excerpt)
			}
		}
		if err != nil {
			if err != io.EOF {
				readErr = err
			}
			break
		}
	}
	if readErr != nil {
		_ = cmd.Cancel()
	}
	waitErr := cmd.Wait()
	status := 0
	if ctx.Err() != nil {
		status = 130
	} else if waitErr != nil {
		var exited *exec.ExitError
		if errors.As(waitErr, &exited) {
			status = processExitCode(exited)
		} else {
			status = 1
		}
	}
	if readErr != nil || file.Sync() != nil {
		if status == 0 {
			status = 1
		}
		return status, errors.New("console artifact capture failed")
	}
	if status != 0 {
		fmt.Fprintln(output, "::group::Suite failure excerpt")
		seen := map[string]bool{}
		for _, line := range append(errorsTail, tail...) {
			if !seen[line] {
				fmt.Fprint(output, line)
				seen[line] = true
			}
		}
		fmt.Fprintln(output, "::endgroup::")
	}
	return status, nil
}
