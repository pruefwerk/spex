package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Gate failures may precede a scope or scenario artifact directory. Retain a
// fixed, secret-safe diagnostic even then, without printing wrapped causes.
func (h *Host) executionDiagnostic(destination io.Writer, err error) {
	reason := "EXECUTION_GATE_FAILED"
	if errors.Is(err, errSuiteNotConfigured) {
		reason = "SUITE_NOT_CONFIGURED"
	}
	diagnostic := struct {
		Schema string `json:"schema"`
		Stage  string `json:"stage"`
		Reason string `json:"reason"`
	}{"spex.execution-diagnostic/v1", "execution_gate", reason}
	data, _ := json.Marshal(diagnostic)
	fmt.Fprintln(destination, string(data))
	directory := filepath.Join(filepath.Dir(h.path), ".spex", "runtime-diagnostics")
	if os.MkdirAll(directory, 0700) != nil {
		return
	}
	// Keep the first gate failure; never follow an existing file or replace
	// the primary outcome if diagnostic persistence itself fails.
	file, openErr := os.OpenFile(filepath.Join(directory, "execution-gate.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if openErr == nil {
		defer file.Close()
		_, _ = file.Write(append(data, '\n'))
	}
}
