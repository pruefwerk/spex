package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/pkg/ownedkind"
	"github.com/pruefwerk/spex/pkg/runtimehost"
)

func TestOwnedKindCleanupStageSurvivesHostWrapping(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "mtb-test")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	// An interrupted creation has no node ledger and must never authorize deletion.
	if err := os.WriteFile(filepath.Join(state, "creation.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	cluster, err := ownedkind.New(ownedkind.Config{
		Name: "mtb-test", StateDirectory: state, Kubeconfig: filepath.Join(state, "kubeconfig"),
		ClusterConfig: filepath.Join(root, "kind.yaml"), ImageRepository: "owned-images",
	})
	if err != nil {
		t.Fatal(err)
	}
	cause := cluster.Cleanup(context.Background())
	h := &Host{path: filepath.Join(root, "host.toml")}
	err = h.cleanupResult(fmt.Errorf("SENTINEL_TOKEN: %w", cause))
	diagnostic := classifyExecutionFailure(err)
	if diagnostic.Stage != "cleanup_node_ledger" || diagnostic.Reason != "NODE_LEDGER_INCOMPLETE" ||
		diagnostic.NodesRemoved == nil || *diagnostic.NodesRemoved != 0 ||
		diagnostic.ImagesRemoved == nil || *diagnostic.ImagesRemoved != 0 {
		t.Fatal("host discarded originating ownership diagnostic")
	}
	var output bytes.Buffer
	h.executionDiagnostic(&output, err)
	if strings.Contains(output.String(), "SENTINEL") || !strings.Contains(output.String(), "NODE_LEDGER_INCOMPLETE") {
		t.Fatal("unsafe or missing originating diagnostic")
	}
}

func TestCleanupAttemptsPersistSafeOutcomesWithoutReplacingFirstGateFailure(t *testing.T) {
	for _, tc := range []struct {
		cause          error
		reason, status string
	}{
		{nil, "CLEANUP_SUCCEEDED", "succeeded"},
		{errors.New("SENTINEL_SIGNED_URL"), "CLEANUP_GATE_FAILED", "failed"},
		{fmt.Errorf("SENTINEL_TOKEN: %w", runtimehost.ErrIdentity), "CLEANUP_IDENTITY_INVALID", "failed"},
		{fmt.Errorf("SENTINEL_TOKEN: %w", runtimehost.ErrPersistence), "CLEANUP_PERSISTENCE_FAILED", "failed"},
		{fmt.Errorf("SENTINEL_TOKEN: %w", runtimehost.ErrAdmission), "CLEANUP_CAPACITY_UNCONFIRMED", "failed"},
		{fmt.Errorf("SENTINEL_TOKEN: %w", runtimehost.ErrCleanup), "CLEANUP_UNCONFIRMED", "failed"},
		{fmt.Errorf("SENTINEL_TOKEN: %w", context.Canceled), "CLEANUP_CANCELLED", "incomplete"},
		{fmt.Errorf("SENTINEL_TOKEN: %w", context.DeadlineExceeded), "CLEANUP_DEADLINE_EXCEEDED", "incomplete"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			h := &Host{path: filepath.Join(t.TempDir(), "host.toml")}
			var output bytes.Buffer
			h.executionDiagnostic(&output, errSuiteNotConfigured)
			first, _ := os.ReadFile(filepath.Join(filepath.Dir(h.path), ".spex/runtime-diagnostics/execution-gate.json"))
			for range 2 {
				err := h.cleanupResult(tc.cause)
				if (err == nil) != (tc.cause == nil) || (err != nil && (!errors.Is(err, tc.cause) || strings.Contains(err.Error(), "SENTINEL"))) {
					t.Fatal("cleanup result or cause changed", err)
				}
				if err != nil {
					h.executionDiagnostic(&output, err)
					if !strings.Contains(output.String(), tc.reason) {
						t.Fatal("CLI lacks cleanup stage/reason")
					}
				}
			}
			files, _ := filepath.Glob(filepath.Join(filepath.Dir(h.path), ".spex/runtime-diagnostics/cleanup-*.json"))
			if len(files) != 2 {
				t.Fatal("cleanup attempts were overwritten")
			}
			for _, path := range files {
				data, err := os.ReadFile(path)
				var report struct{ Schema, Stage, Reason, Status string }
				if err != nil || json.Unmarshal(data, &report) != nil || report.Schema != "spex.cleanup-diagnostic/v1" || report.Reason != tc.reason || report.Status != tc.status {
					t.Fatal("incorrect cleanup evidence", string(data), err)
				}
				info, _ := os.Stat(path)
				if info.Mode().Perm() != 0600 || strings.Contains(string(data), "SENTINEL") || strings.Contains(output.String(), "SENTINEL") {
					t.Fatal("unsafe cleanup evidence or output")
				}
			}
			retained, _ := os.ReadFile(filepath.Join(filepath.Dir(h.path), ".spex/runtime-diagnostics/execution-gate.json"))
			if !bytes.Equal(first, retained) {
				t.Fatal("cleanup overwrote primary gate failure")
			}
		})
	}
}

func TestCleanupEvidenceFailureDoesNotChangePrimaryOutcome(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	h := &Host{path: filepath.Join(blocked, "host.toml")}
	cause := errors.New("SENTINEL_SECRET")
	if err := h.cleanupResult(cause); !errors.Is(err, cause) || strings.Contains(err.Error(), "SENTINEL") {
		t.Fatal("evidence failure replaced cleanup failure")
	}
	if h.cleanupResult(nil) != nil {
		t.Fatal("diagnostic write failure changed successful cleanup")
	}
}
