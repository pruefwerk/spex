package engine

import (
	"encoding/json"
	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSchedulingConnectionResolution(t *testing.T) {
	for _, test := range []struct {
		name       string
		connection *string
		env        map[string]string
		want       string
		invalid    bool
	}{
		{name: "reference", connection: connection("${COORDINATION_URI}"), env: map[string]string{"COORDINATION_URI": "mongodb://store:27017"}, want: "mongodb://store:27017"},
		{name: "literal", connection: connection("mongodb://store:27017"), want: "mongodb://store:27017"},
		{name: "srv", connection: connection("mongodb+srv://store.example"), want: "mongodb+srv://store.example"},
		{name: "unconfigured", connection: connection("${COORDINATION_URI}")},
		{name: "empty", connection: connection(""), env: map[string]string{"SPEX_SCHEDULING_MONGODB_URI": "mongodb://legacy:27017"}},
		{name: "legacy", env: map[string]string{"SPEX_SCHEDULING_MONGODB_URI": "mongodb://legacy:27017"}, want: "mongodb://legacy:27017"},
		{name: "partial template", connection: connection("${COORDINATION_URI}/database"), invalid: true},
		{name: "invalid uri", connection: connection("sentinel-secret-not-a-uri"), invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, file := configuredFixture(t)
			d, err := hostconfig.Load(file)
			if err != nil {
				t.Fatal(err)
			}
			h, err := New(file, "test")
			if err != nil {
				t.Fatal(err)
			}
			policy := schedulingPolicy{Schema: hostconfig.SchedulingPolicySchema, ConnectionString: test.connection, Database: "runtime", Collection: "scheduling", Pool: d.SchedulingPool, Capacity: 1}
			data, _ := json.Marshal(policy)
			if err := os.WriteFile(filepath.Join(root, d.SchedulingPolicy), data, 0600); err != nil {
				t.Fatal(err)
			}
			_, uri, err := h.resolveScheduling(root, func(key string) string { return test.env[key] })
			if (err != nil) != test.invalid || uri != test.want {
				t.Fatalf("resolution mismatch for %s", test.name)
			}
			if err != nil && strings.Contains(err.Error(), "sentinel-secret") {
				t.Fatal("secret leaked")
			}
		})
	}
}

func connection(value string) *string { return &value }

func TestCapacityModeUsesDeclaredConnection(t *testing.T) {
	root, file := configuredFixture(t)
	d, err := hostconfig.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(file, "test")
	if err != nil {
		t.Fatal(err)
	}
	paths, err := h.prepareExecution(root, d.Suite, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	policy := schedulingPolicy{Schema: hostconfig.SchedulingPolicySchema, ConnectionString: connection("${CUSTOM_STORE}"), Database: "runtime", Collection: "scheduling", Pool: d.SchedulingPool, Capacity: 1}
	data, _ := json.Marshal(policy)
	if err := os.WriteFile(filepath.Join(root, d.SchedulingPolicy), data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		env := func(key string) string {
			if key == "CUSTOM_STORE" && enabled {
				return "mongodb://store:27017"
			}
			if key == "SPEX_SCHEDULING_MONGODB_URI" {
				return "mongodb://ignored:27017"
			}
			return ""
		}
		identity, _, err := h.capacityIdentity(root, paths.Scope, env)
		want := "disabled"
		if enabled {
			want = "enabled"
		}
		if err != nil || identity.Mode != want {
			t.Fatal("capacity and connection disagree", err)
		}
	}
}

func TestMissingSchedulingPolicyOnlyAllowsUnconfiguredStore(t *testing.T) {
	root, file := configuredFixture(t)
	d, err := hostconfig.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(file, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.resolveScheduling(root, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.resolveScheduling(root, func(string) string { return "mongodb://store:27017" }); err == nil {
		t.Fatal("configured store admitted without policy")
	}
	if err := os.WriteFile(filepath.Join(root, d.SchedulingPolicy), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.resolveScheduling(root, func(string) string { return "" }); err == nil {
		t.Fatal("invalid declared policy ignored")
	}
}
