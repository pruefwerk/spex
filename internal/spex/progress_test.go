package spex

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type acknowledgingWriter struct {
	bytes.Buffer
	ackPath string
}

func (w *acknowledgingWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if strings.Contains(w.String(), "early output") {
		if writeErr := os.WriteFile(w.ackPath, nil, 0600); writeErr != nil {
			return n, writeErr
		}
	}
	return n, err
}

func TestExecuteKUTTLStreamsBeforeProcessExit(t *testing.T) {
	workspace := t.TempDir()
	ack := filepath.Join(workspace, "ack")
	command := filepath.Join(workspace, "fake-kuttl")
	// The child cannot complete successfully until its live output is observed.
	// Bound the wait so the old buffered implementation fails instead of hanging.
	script := fmt.Sprintf(`#!/bin/sh
echo 'early output'
i=0
while [ ! -f %q ]; do
  i=$((i + 1))
  [ "$i" -lt 100 ] || exit 65
  sleep 0.01
done
echo 'stderr output' >&2
echo 'final output'
`, ack)
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	stdout := &acknowledgingWriter{ackPath: ack}
	var stderr bytes.Buffer
	result := executeKUTTL(command, workspace, "", stdout, &stderr)
	if result.Err != nil {
		t.Fatalf("live output was not acknowledged before exit: %v; %s", result.Err, stdout.String())
	}
	for _, line := range []string{"early output", "stderr output", "final output"} {
		if strings.Count(stdout.String(), line) != 1 || strings.Count(result.Output, line) != 1 {
			t.Fatalf("output missing or duplicated: live=%q captured=%q", stdout.String(), result.Output)
		}
	}
}

type countingWriter int

func (w *countingWriter) Write(p []byte) (int, error) {
	*w += countingWriter(len(p))
	return len(p), nil
}

func TestExecuteKUTTLLiveOutputExceedsCaptureLimit(t *testing.T) {
	workspace := t.TempDir()
	command := filepath.Join(workspace, "fake-kuttl")
	size := maxKUTTLOutputSize + 1024
	script := fmt.Sprintf("#!/bin/sh\nhead -c %d /dev/zero | tr '\\000' x\n", size)
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var stdout countingWriter
	result := executeKUTTL(command, workspace, "", &stdout, io.Discard)
	if result.Err != nil || int64(stdout) != size {
		t.Fatalf("live output lost: bytes=%d error=%v", stdout, result.Err)
	}
	if !strings.Contains(result.Output, "output truncated after") || int64(len(result.Output)) > maxKUTTLOutputSize+100 {
		t.Fatalf("capture bound not preserved: %d bytes", len(result.Output))
	}
}
