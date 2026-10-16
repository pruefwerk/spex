package ownedkind

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type cleanupFaultRunner struct {
	base  *daemon
	match []string
	cause error
}

func (r cleanupFaultRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) >= len(r.match) && slices.Equal(args[:len(r.match)], r.match) {
		return "SENTINEL_SIGNED_URL", r.cause
	}
	return r.base.Run(ctx, args...)
}

func TestCleanupClassifiesEveryStageWithoutWeakeningOwnership(t *testing.T) {
	for _, tc := range []struct {
		reason, stage string
		command       []string
		mutate        string
		nodesRemoved  int
	}{
		{"NODE_LEDGER_INCOMPLETE", "cleanup_node_ledger", nil, "node-ledger", 0},
		{"NODE_OWNERSHIP_UNCONFIRMED", "cleanup_node_ownership", []string{"docker", "ps"}, "", 0},
		{"NODE_OWNERSHIP_UNCONFIRMED", "cleanup_node_ownership", nil, "foreign-node", 0},
		{"NODE_DELETION_FAILED", "cleanup_node_deletion", []string{"docker", "rm"}, "", 0},
		{"IMAGE_LEDGER_INVALID", "cleanup_image_ledger", nil, "image-ledger", 1},
		{"IMAGE_LOOKUP_FAILED", "cleanup_image_lookup", []string{"docker", "image", "ls"}, "", 1},
		{"IMAGE_INSPECTION_FAILED", "cleanup_image_inspection", []string{"docker", "image", "inspect"}, "", 1},
		{"IMAGE_OWNERSHIP_CHANGED", "cleanup_image_ownership", nil, "foreign-image", 1},
		{"IMAGE_DELETION_FAILED", "cleanup_image_deletion", []string{"docker", "image", "rm"}, "", 1},
	} {
		t.Run(tc.reason+tc.mutate, func(t *testing.T) {
			d := &daemon{}
			c := fixture(t, "worker-a", d)
			ctx := context.Background()
			if err := c.Create(ctx); err != nil {
				t.Fatal(err)
			}
			if err := c.LoadImage(ctx, Image{Alias: "probe:kind", Source: "registry.example/probe:commit"}); err != nil {
				t.Fatal(err)
			}
			tag, key := c.imageTag("probe:kind")
			switch tc.mutate {
			case "node-ledger":
				if err := os.Remove(filepath.Join(c.config.StateDirectory, "nodes.json")); err != nil {
					t.Fatal(err)
				}
			case "image-ledger":
				if err := os.Remove(filepath.Join(c.config.StateDirectory, "image-id-"+key+".json")); err != nil {
					t.Fatal(err)
				}
			case "foreign-node":
				d.wrongInspection = true
			case "foreign-image":
				d.images[tag] = "sha256:" + nodeID("replacement")
			}
			cause := errors.New("SENTINEL_TOKEN")
			if tc.command != nil {
				c.run = cleanupFaultRunner{d, tc.command, cause}
			}
			err := c.Cleanup(ctx)
			var failure *CleanupError
			if !errors.As(err, &failure) || strings.Contains(err.Error(), "SENTINEL") {
				t.Fatal("missing or unsafe typed failure", err)
			}
			stage, reason, nodes, images := failure.Diagnostic()
			if stage != tc.stage || reason != tc.reason || nodes != tc.nodesRemoved || images != 0 {
				t.Fatal("wrong cleanup stage or partial-deletion counts", stage, reason, nodes, images)
			}
			if tc.command != nil && !errors.Is(err, cause) {
				t.Fatal("originating cause discarded")
			}
			if len(d.nodes) != 1-tc.nodesRemoved || d.images[tag] == "" {
				t.Fatal("failure altered resource-deletion boundary")
			}
		})
	}
}

func TestCleanupErrorZeroValueUsesFixedFallback(t *testing.T) {
	for _, failure := range []*CleanupError{nil, {reason: "SENTINEL_SECRET", nodesRemoved: 99}} {
		stage, reason, nodes, images := failure.Diagnostic()
		if stage != "cleanup" || reason != "CLEANUP_FAILED" || nodes != 0 || images != 0 {
			t.Fatal("unvalidated diagnostic exposed")
		}
	}
}
