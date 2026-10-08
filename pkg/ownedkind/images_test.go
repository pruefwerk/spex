package ownedkind

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImagesUsePrivateBuildTagsAndNodeLocalAliases(t *testing.T) {
	d := &daemon{}
	c := fixture(t, "worker-a", d)
	ctx := context.Background()
	if err := c.Create(ctx); err != nil {
		t.Fatal(err)
	}
	build := &Build{Context: t.TempDir(), Dockerfile: filepath.Join(t.TempDir(), "Dockerfile"), Arguments: map[string]string{"BASE_IMAGE": "registry.example/base:commit"}, Contexts: map[string]string{"fixture": t.TempDir()}}
	if err := c.LoadImage(ctx, Image{Alias: "fixture-probe:kind", Build: build}); err != nil {
		t.Fatal(err)
	}
	for _, args := range d.calls {
		if args[0] == "docker" && args[1] == "build" && !strings.HasPrefix(argument(args, "-t"), "fixture:worker-a-") {
			t.Fatal("unscoped build tag")
		}
		if args[0] == "docker" && args[1] == "exec" && (args[2] != nodeID("worker-a") || args[len(args)-1] != "docker.io/library/fixture-probe:kind") {
			t.Fatal("alias escaped owned node")
		}
	}
	if err := c.LoadImage(ctx, Image{Alias: "fixture-probe:kind", Build: build}); err == nil {
		t.Fatal("retried image attempt")
	}
}

func TestExistingPrivateTagIsNotReplaced(t *testing.T) {
	d := &daemon{}
	c := fixture(t, "worker-a", d)
	ctx := context.Background()
	if err := c.Create(ctx); err != nil {
		t.Fatal(err)
	}
	tag, _ := c.imageTag("probe:kind")
	d.images[tag] = "sha256:" + nodeID("existing")
	before := len(d.calls)
	if err := c.LoadImage(ctx, Image{Alias: "probe:kind", Source: "registry.example/probe:commit"}); err == nil {
		t.Fatal("replaced private tag")
	}
	for _, args := range d.calls[before:] {
		if args[1] == "pull" || args[1] == "tag" || args[1] == "build" {
			t.Fatal("mutated existing image")
		}
	}
}

func TestChangedImageIdentityBlocksDeletion(t *testing.T) {
	d := &daemon{}
	c := fixture(t, "worker-a", d)
	ctx := context.Background()
	if err := c.Create(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.LoadImage(ctx, Image{Alias: "probe:kind", Source: "registry.example/probe:commit"}); err != nil {
		t.Fatal(err)
	}
	tag, _ := c.imageTag("probe:kind")
	d.images[tag] = "sha256:" + nodeID("replacement")
	if err := c.Cleanup(ctx); err == nil {
		t.Fatal("changed image deleted")
	}
	if d.images[tag] == "" {
		t.Fatal("replacement deleted")
	}
}

func TestIncompleteImagePreparationRequiresReview(t *testing.T) {
	d := &daemon{}
	c := fixture(t, "worker-a", d)
	ctx := context.Background()
	if err := c.Create(ctx); err != nil {
		t.Fatal(err)
	}
	tag, key := c.imageTag("probe:kind")
	if err := writeNew(filepath.Join(c.config.StateDirectory, "image-"+key+".json"), imageAttempt{tag, "probe:kind"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Cleanup(ctx); err == nil {
		t.Fatal("incomplete preparation accepted")
	}
	if len(d.nodes) != 0 {
		t.Fatal("image uncertainty prevented verified node cleanup")
	}
}

func TestEmptyNodeLedgerMustBeArrayAndInvalidImageDoesNotMutate(t *testing.T) {
	d := &daemon{}
	c := fixture(t, "worker-a", d)
	if err := os.WriteFile(filepath.Join(c.config.StateDirectory, "nodes.json"), []byte("null"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OwnedNodes(context.Background()); err == nil {
		t.Fatal("null ledger accepted")
	}
	if err := c.LoadImage(context.Background(), Image{Alias: "probe:kind", Build: &Build{}}); err == nil || len(d.calls) != 0 {
		t.Fatal("invalid image triggered daemon commands")
	}
}

func TestImageNormalizationRetainsRegistryAndTag(t *testing.T) {
	for input, want := range map[string]string{"probe:kind": "docker.io/library/probe:kind", "team/probe:kind": "docker.io/team/probe:kind", "registry.example/team/probe:commit": "registry.example/team/probe:commit", "localhost:5000/probe:kind": "localhost:5000/probe:kind"} {
		if NormalizedImage(input) != want {
			t.Fatal(input)
		}
	}
}
