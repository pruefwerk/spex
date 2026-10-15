package engine

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionGateDiagnosticsAreSafeAndRetainTheFirstFailure(t *testing.T) {
	for _, tc := range []struct {
		cause  error
		reason string
	}{
		{fmt.Errorf("PRIVATE_TOKEN_SIGNED_URL: %w", errSuiteNotConfigured), "SUITE_NOT_CONFIGURED"},
		{errors.New("PRIVATE_TOKEN_SIGNED_URL"), "EXECUTION_GATE_FAILED"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			root := t.TempDir()
			h := &Host{path: filepath.Join(root, "host.toml")}
			var output bytes.Buffer
			h.executionDiagnostic(&output, tc.cause)
			data, err := os.ReadFile(filepath.Join(root, ".spex/runtime-diagnostics/execution-gate.json"))
			if err != nil || strings.Contains(output.String(), "PRIVATE") || strings.Contains(string(data), "PRIVATE") || !strings.Contains(string(data), tc.reason) {
				t.Fatal("unsafe or missing gate diagnostic", err)
			}
			h.executionDiagnostic(&output, errors.New("different failure"))
			retained, err := os.ReadFile(filepath.Join(root, ".spex/runtime-diagnostics/execution-gate.json"))
			if err != nil || !bytes.Equal(retained, data) {
				t.Fatal("later failure replaced the first diagnostic", err)
			}
		})
	}
}
