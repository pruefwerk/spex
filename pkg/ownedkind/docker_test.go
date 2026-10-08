package ownedkind

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This smoke test uses stopped Docker containers, not a running Kind cluster.
// It verifies real daemon ownership/deletion without downloading or starting
// application images. Full Kind creation requires separate qualification.
func TestLiveDockerOwnership(t *testing.T) {
	image := os.Getenv("SPEX_OWNED_KIND_TEST_IMAGE")
	if image == "" {
		t.Skip("set SPEX_OWNED_KIND_TEST_IMAGE to a cached image for live Docker qualification")
	}
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stop()
	runner := CommandRunner{}
	id, err := runner.Run(ctx, "docker", "image", "inspect", image, "--format", "{{.Id}}")
	id = strings.TrimSpace(id)
	if err != nil || !imageID.MatchString(id) {
		t.Fatal("qualification requires a cached image", err)
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	name := "ownedkind-" + hex.EncodeToString(random)
	c := fixture(t, name, &daemon{})
	c.run = runner
	create := func(owner string) string {
		value, err := runner.Run(ctx, "docker", "create", "--name", owner+"-node", "--label", "io.x-k8s.kind.cluster="+owner, "--label", "spex.pruefwerk.dev/qualification="+name, id, "/bin/true")
		value = strings.TrimSpace(value)
		if err != nil || !nodePattern.MatchString(value) {
			t.Fatal("disposable container creation failed", err)
		}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, _ = runner.Run(cleanup, "docker", "rm", "--force", "--volumes", value)
		})
		return value
	}
	own := create(name)
	other := create(name + "-other")
	if err := writeNew(filepath.Join(c.config.StateDirectory, "nodes.json"), []string{own}); err != nil {
		t.Fatal(err)
	}
	tag, key := c.imageTag("probe:kind")
	otherTag := tag + "-other"
	for _, private := range []string{tag, otherTag} {
		present, err := runner.Run(ctx, "docker", "image", "ls", "--quiet", private)
		if err != nil || strings.TrimSpace(present) != "" {
			t.Fatal("qualification tag already exists", err)
		}
		if _, err := runner.Run(ctx, "docker", "tag", id, private); err != nil {
			t.Fatal(err)
		}
		private := private
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, _ = runner.Run(cleanup, "docker", "image", "rm", private)
		})
	}
	if err := writeNew(filepath.Join(c.config.StateDirectory, "image-"+key+".json"), imageAttempt{tag, "probe:kind"}); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(filepath.Join(c.config.StateDirectory, "image-id-"+key+".json"), imageRecord{tag, id}); err != nil {
		t.Fatal(err)
	}
	if err := c.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, "docker", "inspect", other); err != nil {
		t.Fatal("removed peer container", err)
	}
	if present, err := runner.Run(ctx, "docker", "image", "ls", "--quiet", otherTag); err != nil || strings.TrimSpace(present) == "" {
		t.Fatal("removed peer image tag", err)
	}
	if present, err := runner.Run(ctx, "docker", "image", "ls", "--quiet", tag); err != nil || strings.TrimSpace(present) != "" {
		t.Fatal("owned image tag retained", err)
	}
	if _, err := runner.Run(ctx, "docker", "image", "inspect", id); err != nil {
		t.Fatal("removed shared cached image", err)
	}
	if err := c.Cleanup(ctx); err != nil {
		t.Fatal("repeat cleanup failed", err)
	}
}
