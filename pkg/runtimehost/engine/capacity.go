package engine

import (
	"context"
	"errors"
	"fmt"
	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pruefwerk/spex/pkg/receiver"
	"github.com/pruefwerk/spex/pkg/runtimehost"
	"github.com/pruefwerk/spex/pkg/scheduling"
)

// The SDK retains the checkpoint encoding; this host owns its scope policy.
type capacityContext = runtimehost.Identity

func (h *Host) readCapacity(path string) (capacityContext, string, error) {
	value, err := runtimehost.ReadIdentity(path)
	root, rootErr := filepath.Abs(".")
	if err != nil || rootErr != nil || value.Schema != h.resolver.Protocol("capacity-context") || value.Root != root ||
		value.Mode != "enabled" || !regexp.MustCompile(h.scopePattern()).MatchString(value.ScopeName) ||
		!requestID.MatchString(value.Request) || value.Worker == "" {
		return value, "", errors.New("capacity context invalid")
	}
	directory := filepath.Join(root, ".spex", "capacity", value.ScopeName)
	if path != filepath.Join(directory, "context.json") || value.ScopePath != filepath.Join(root, ".spex", "execution-scopes", value.ScopeName+".json") {
		return value, "", errors.New("capacity context is not execution-owned")
	}
	return value, filepath.Join(directory, "scheduling.json"), nil
}

func (h *Host) capacityCommand(ctx context.Context, operation, path string) int {
	return h.capacityWithStore(ctx, operation, path, h.openScheduling)
}
func (h *Host) capacityWithStore(ctx context.Context, operation, path string, open func(context.Context, string, string) (*receiver.Scheduling, func(), error)) int {
	return h.capacityWithCleanup(ctx, operation, path, open, func(ctx context.Context, root, scope string) error {
		return h.kindOperation(ctx, root, scope, "cleanup")
	})
}
func (h *Host) capacityWithCleanup(ctx context.Context, operation, path string, open func(context.Context, string, string) (*receiver.Scheduling, func(), error), cleanupOwned func(context.Context, string, string) error) int {
	value, reportPath, err := h.readCapacity(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Capacity context invalid")
		return 1
	}
	capacity := runtimehost.Capacity{
		Identity: value, ReportPath: reportPath, CleanupTimeout: hostconfig.Deadline(h.config.CleanupTimeout), ReleaseTimeout: 15 * time.Second,
		Open: func(ctx context.Context) (*scheduling.Scheduler, func(), error) {
			admitted, closeStore, err := open(ctx, value.Root, value.Worker)
			if err != nil || admitted == nil {
				return nil, closeStore, runtimehost.ErrAdmission
			}
			return admitted.Scheduler, closeStore, nil
		},
		Cleanup: func(ctx context.Context) error { return cleanupOwned(ctx, value.Root, value.ScopePath) },
	}
	if err := capacity.Run(ctx, runtimehost.Operation(strings.TrimPrefix(operation, "capacity-"))); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, runtimehost.ErrOperation) {
			return 2
		}
		if operation == "capacity-acquire" && ctx.Err() != nil {
			return 130
		}
		return 1
	}
	return 0
}
