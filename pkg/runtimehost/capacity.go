package runtimehost

import (
	"context"
	"os"
	"time"

	"github.com/pruefwerk/spex/pkg/scheduling"
)

// Capacity resumes the same durable ticket across host steps. Store selection
// and environment cleanup are supplied by the host, not by caller definitions.
type Capacity struct {
	Identity       Identity
	ReportPath     string
	Open           func(context.Context) (*scheduling.Scheduler, func(), error)
	Cleanup        func(context.Context) error
	CleanupTimeout time.Duration
	ReleaseTimeout time.Duration
}

func (c Capacity) Run(ctx context.Context, operation Operation) error {
	if operation != Acquire && operation != Check && operation != Finish {
		return ErrOperation
	}
	if !c.Identity.valid() || c.Identity.Mode != "enabled" || c.Open == nil || c.ReportPath == "" ||
		c.CleanupTimeout <= 0 || c.ReleaseTimeout <= 0 {
		return ErrIdentity
	}
	scheduler, closeStore, err := c.Open(ctx)
	if err != nil || scheduler == nil || closeStore == nil {
		if closeStore != nil {
			closeStore()
		}
		return ErrAdmission
	}
	defer closeStore()
	if operation == Acquire {
		if _, err := os.Lstat(c.ReportPath); !os.IsNotExist(err) {
			return ErrIdentity
		}
		lease, report, err := scheduler.Acquire(ctx, c.Identity.Request, c.Identity.Worker)
		if replacePrivate(c.ReportPath, report) != nil {
			if lease != nil {
				cleanup, cancel := context.WithTimeout(context.Background(), c.ReleaseTimeout)
				defer cancel()
				_ = lease.Finish(cleanup, true) // Environment creation has not started.
			}
			return ErrPersistence
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrAdmission
		}
		return nil
	}
	var report scheduling.Report
	if readPrivate(c.ReportPath, &report) != nil || report.Request != c.Identity.Request ||
		report.Worker != c.Identity.Worker {
		return ErrIdentity
	}
	if operation == Finish && report.Status == "released" {
		return nil
	}
	lease, err := scheduler.Resume(ctx, report)
	if err != nil {
		return ErrAdmission
	}
	if operation == Check {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.Background(), c.CleanupTimeout)
	safe := c.Cleanup != nil && c.Cleanup(cleanup) == nil
	cancel()
	release, stop := context.WithTimeout(context.Background(), c.ReleaseTimeout)
	defer stop()
	report.Status = "released"
	if !safe {
		report.Status = "recovery_required"
	}
	if lease.Finish(release, safe) != nil {
		report.Status = "unknown"
	}
	if replacePrivate(c.ReportPath, report) != nil {
		return ErrPersistence
	}
	if report.Status != "released" {
		return ErrCleanup
	}
	return nil
}
