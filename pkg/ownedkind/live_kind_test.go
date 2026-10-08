package ownedkind

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Use an explicitly selected cached node image: qualification must not silently
// fetch a different platform image or replace a shared daemon tag.
type liveKindRunner struct{ nodeImage string }

func (r liveKindRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) >= 3 && args[0] == "kind" && args[1] == "create" && args[2] == "cluster" {
		args = append(append([]string{}, args...), "--image", r.nodeImage)
	}
	return (CommandRunner{}).Run(ctx, args...)
}

func TestLiveKindLifecycle(t *testing.T) {
	image := os.Getenv("SPEX_LIVE_KIND_NODE_IMAGE")
	if image == "" {
		t.Skip("set SPEX_LIVE_KIND_NODE_IMAGE to a cached node image for live Kind qualification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	runner := liveKindRunner{image}
	if _, err := runner.Run(ctx, "docker", "image", "inspect", image); err != nil {
		t.Fatal("cached node image unavailable", err)
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	name := "ownedkind-" + hex.EncodeToString(nonce)
	first := fixture(t, name, &daemon{})
	second := fixture(t, name+"-peer", &daemon{})
	for _, cluster := range []*Cluster{first, second} {
		cluster.run = runner
		cluster := cluster
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 2*time.Minute)
			defer stop()
			if err := cluster.Cleanup(cleanup); err != nil {
				t.Errorf("live cleanup requires ledger review: %v", err)
			}
		})
		if err := cluster.Create(ctx); err != nil {
			t.Fatal("live Kind creation failed", err)
		}
		nodes, err := cluster.OwnedNodes(ctx)
		if err != nil || len(nodes) != 1 {
			t.Fatal("unexpected live node ledger", nodes, err)
		}
		if _, err := os.Stat(cluster.config.Kubeconfig); err != nil {
			t.Fatal("private kubeconfig missing", err)
		}
	}
	build := filepath.Join(first.config.StateDirectory, "build")
	if err := os.Mkdir(build, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(build, "Dockerfile"), []byte("FROM scratch\nCOPY proof /proof\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(build, "proof"), []byte("owned-kind-qualification\n"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := "owned-kind-proof:kind"
	if err := first.LoadImage(ctx, Image{Alias: alias, Build: &Build{Context: build, Dockerfile: filepath.Join(build, "Dockerfile")}}); err != nil {
		t.Fatal("live private image loading failed", err)
	}
	if present, err := runner.Run(ctx, "docker", "image", "ls", "--quiet", alias); err != nil || present != "" {
		t.Fatal("compatibility alias leaked onto daemon", err)
	}
	if err := first.Cleanup(ctx); err != nil {
		t.Fatal("live exact-node cleanup failed", err)
	}
	if nodes, err := first.OwnedNodes(ctx); err != nil || len(nodes) != 0 {
		t.Fatal("own node retained", nodes, err)
	}
	if nodes, err := second.OwnedNodes(ctx); err != nil || len(nodes) != 1 {
		t.Fatal("peer node removed", nodes, err)
	}
	if _, err := runner.Run(ctx, "docker", "image", "inspect", image); err != nil {
		t.Fatal("cached node image removed", err)
	}
}
