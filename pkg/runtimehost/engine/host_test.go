package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pelletier/go-toml/v2"
	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
)

func TestHostsOwnIndependentSnapshots(t *testing.T) {
	_, file := configuredFixture(t)
	first, err := New(file, "first")
	if err != nil {
		t.Fatal(err)
	}
	d := first.resolver.Snapshot()
	d.Groups = []string{"baseline", "second"}
	d.ScopePrefix = "second"
	d.SchemaPrefix = "second"
	data, _ := toml.Marshal(d)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	second, err := New(file, "second")
	if err != nil {
		t.Fatal(err)
	}
	before := hostconfig.Current()
	defer hostconfig.Bind(before)
	hostconfig.Bind(hostconfig.Definition{})
	if first.version != "first" || second.version != "second" || !first.allowedGroupTags()["group-change"] || first.allowedGroupTags()["group-second"] || !second.allowedGroupTags()["group-second"] {
		t.Fatal("host policy or release crossed instances")
	}
	view := first.resolver.Snapshot()
	view.Groups[1] = "mutated"
	if !first.allowedGroupTags()["group-change"] {
		t.Fatal("snapshot mutation changed host")
	}
	scope, err := first.resolver.Allocate("owner/runtime", 1, 1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "kind")
	if err != nil {
		t.Fatal(err)
	}
	name, err := scope.Name()
	if err != nil || name[:5] != "spex-" {
		t.Fatal("scope depends on global binding", name, err)
	}
}

func TestHostOperationsRestoreEnvironmentAndCompatibilityBinding(t *testing.T) {
	root, file := configuredFixture(t)
	t.Chdir(root)
	h, err := New(file, "test")
	if err != nil {
		t.Fatal(err)
	}
	before := hostconfig.Current()
	for _, key := range []string{hostconfig.ConfigEnvironment, h.config.ScopeEnvironment, "KUBECONFIG", "SPEX_SUITE"} {
		t.Setenv(key, "parent-value")
	}
	assertRestored := func() {
		t.Helper()
		for _, key := range []string{hostconfig.ConfigEnvironment, h.config.ScopeEnvironment, "KUBECONFIG", "SPEX_SUITE"} {
			if os.Getenv(key) != "parent-value" {
				t.Fatal("environment leaked", key)
			}
		}
		if !reflect.DeepEqual(hostconfig.Current(), before) {
			t.Fatal("host changed compatibility binding")
		}
	}
	var out bytes.Buffer
	if h.Run(context.Background(), []string{"validate"}, &out, &out) != 0 {
		t.Fatal(out.String())
	}
	assertRestored()
	if h.Run(context.Background(), []string{"config", "select", "--event", "push", "--message", "[kind-group:one]"}, &out, &out) != 2 {
		t.Fatal("invalid selection accepted")
	}
	assertRestored()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if h.Run(ctx, []string{"execution", "prepare-start", "--suite", h.config.Suite}, &out, &out) != 130 {
		t.Fatal("cancelled operation succeeded", out.String())
	}
	assertRestored()
	creations, _ := filepath.Glob(filepath.Join(root, ".spex/isolated/*/creation.json"))
	if len(creations) != 0 {
		t.Fatal("cancelled host created a cluster")
	}
}

func TestHostRefusesConcurrentEnvironmentOperations(t *testing.T) {
	_, file := configuredFixture(t)
	h, err := New(file, "test")
	if err != nil {
		t.Fatal(err)
	}
	hostProcess.Lock()
	defer hostProcess.Unlock()
	var out bytes.Buffer
	if h.Run(context.Background(), []string{"validate"}, &out, &out) != 2 {
		t.Fatal("concurrent environment operation accepted")
	}
}
