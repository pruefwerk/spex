package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/pruefwerk/spex/pkg/ownedkind"
	"github.com/pruefwerk/spex/pkg/runtimehost"
)

type cleanupFailure struct{ cause error }

func (e *cleanupFailure) Error() string { return "execution cleanup failed" }
func (e *cleanupFailure) Unwrap() error { return e.cause }

type executionFailureDiagnostic struct {
	Schema        string `json:"schema"`
	Stage         string `json:"stage"`
	Reason        string `json:"reason"`
	NodesRemoved  *int   `json:"nodes_removed,omitempty"`
	ImagesRemoved *int   `json:"images_removed,omitempty"`
}

func classifyExecutionFailure(err error) executionFailureDiagnostic {
	d := executionFailureDiagnostic{Schema: "spex.execution-diagnostic/v1", Stage: "execution_gate", Reason: "EXECUTION_GATE_FAILED"}
	var cleanup *cleanupFailure
	if !errors.As(err, &cleanup) {
		if errors.Is(err, errSuiteNotConfigured) {
			d.Reason = "SUITE_NOT_CONFIGURED"
		}
		return d
	}
	d.Stage, d.Reason = "cleanup_gate", "CLEANUP_GATE_FAILED"
	var owned *ownedkind.CleanupError
	if errors.As(err, &owned) && owned != nil {
		stage, reason, nodes, images := owned.Diagnostic()
		d.Stage, d.Reason = stage, reason
		d.NodesRemoved, d.ImagesRemoved = &nodes, &images
		return d
	}
	switch {
	case errors.Is(err, runtimehost.ErrIdentity):
		d.Stage, d.Reason = "cleanup_identity", "CLEANUP_IDENTITY_INVALID"
	case errors.Is(err, runtimehost.ErrPersistence):
		d.Stage, d.Reason = "cleanup_persistence", "CLEANUP_PERSISTENCE_FAILED"
	case errors.Is(err, runtimehost.ErrAdmission):
		d.Stage, d.Reason = "cleanup_capacity", "CLEANUP_CAPACITY_UNCONFIRMED"
	case errors.Is(err, context.DeadlineExceeded):
		d.Stage, d.Reason = "cleanup", "CLEANUP_DEADLINE_EXCEEDED"
	case errors.Is(err, context.Canceled):
		d.Stage, d.Reason = "cleanup", "CLEANUP_CANCELLED"
	case errors.Is(err, runtimehost.ErrCleanup):
		d.Stage, d.Reason = "cleanup", "CLEANUP_UNCONFIRMED"
	}
	return d
}

// Retain every cleanup attempt separately from the admission success marker.
// Failed attempts must not replace the primary result or authorize a release.
// Persistence is best effort and must not change the existing cleanup outcome.
func (h *Host) cleanupResult(err error) error {
	var failure error
	status := "succeeded"
	d := executionFailureDiagnostic{Schema: "spex.cleanup-diagnostic/v1", Stage: "cleanup", Reason: "CLEANUP_SUCCEEDED"}
	if err != nil {
		failure = &cleanupFailure{cause: err}
		d = classifyExecutionFailure(failure)
		d.Schema = "spex.cleanup-diagnostic/v1"
		status = "failed"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = "incomplete"
		}
	}
	directory := filepath.Join(filepath.Dir(h.path), ".spex", "runtime-diagnostics")
	if os.MkdirAll(directory, 0700) != nil {
		return failure
	}
	file, openErr := os.CreateTemp(directory, "cleanup-*.json")
	if openErr == nil {
		defer file.Close()
		_ = json.NewEncoder(file).Encode(struct {
			executionFailureDiagnostic
			Status string `json:"status"`
		}{d, status})
		_ = file.Sync()
	}
	return failure
}
