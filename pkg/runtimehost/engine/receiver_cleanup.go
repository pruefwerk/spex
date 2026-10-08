package engine

import (
	"context"
	"errors"
	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
	"os"
	"path/filepath"
)

// Workflow recovery may delete only this admission's recorded resources. It
// does not infer that a receiver-managed scheduler ticket is safe to release.
func (h *Host) cleanupReceiver(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	path := filepath.Join(root, ".spex/receiver-private/context.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	var admitted admissionContext
	if readJSON(path, &admitted) != nil || admitted.Schema != h.resolver.Protocol("receiver-admission") || admitted.Root != root {
		return errors.New("invalid cleanup admission context")
	}
	if _, err := h.nativeKind(root, admitted.Scope, nil); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), hostconfig.Deadline(h.config.CleanupTimeout))
	defer cancel()
	return h.gateCapacity(ctx, "finish", root, admitted.Scope, os.Getenv, h.capacityCommand, func(ctx context.Context, root, scope string) error {
		return h.kindOperation(ctx, root, scope, "cleanup")
	})
}
