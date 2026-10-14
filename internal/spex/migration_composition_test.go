package spex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pruefwerk/spex/internal/workspace"
	"github.com/pruefwerk/spex/pkg/scenario"
	"github.com/pruefwerk/spex/pkg/scenarioruntime"
	"gopkg.in/yaml.v3"
)

func TestCallerFeatureInheritsHostConfigurationAndOverridesBinding(t *testing.T) {
	host, caller := repoRoot(t), t.TempDir()
	feature := `Feature: Caller acceptance
  Scenario: Uses shared catalog
    Given tenant "tenant-dev"
    And device "device-dev-1"
    When device "device-dev-1" publishes energy reading 42.5 as "reading-1"
    Then Redpanda contains reading "reading-1" with value 42.5
`
	if err := os.WriteFile(filepath.Join(caller, "test.feature"), []byte(feature), 0600); err != nil {
		t.Fatal(err)
	}
	binding := `spec:
  mqtt:
    brokerURL: tcp://caller:1883
  scenarioParameters:
    tenantId: caller-tenant
  rbac:
    create: false
`
	if err := os.WriteFile(filepath.Join(caller, "binding.yaml"), []byte(binding), 0600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewReceiverMigrationRuntimeWithSources(host, "examples/suites/mqtt-local.yaml", "v1.0.0", ReceiverMigrationSources{Directory: caller, RequiredNamespace: "spex-test", RequiredKubeContext: "local-dev"})
	if err != nil {
		t.Fatal(err)
	}
	config, err := scenario.ParseRuntimeConfig([]byte("[configuration]\nbinding = 'binding.yaml'\n"))
	if err != nil {
		t.Fatal(err)
	}
	file := "test.feature"
	request := scenarioruntime.ResolveRequest{Workspace: host, Scenario: scenario.Scenario{Schema: scenario.Schema, Runtime: runtime.ID(), RuntimeConfig: config, Tests: []scenario.TestSource{{File: &file}}}}
	prepared, err := runtime.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	resolved := prepared.(*preparedMigrationRuntime)
	if len(resolved.inputs) != 1 {
		t.Fatal("caller feature was not selected")
	}
	input := resolved.inputs[0]
	if input.Binding.Spec.MQTT.BrokerURL != "tcp://caller:1883" || input.Binding.Spec.MQTT.CredentialsRef != "mqtt-credentials" || input.Binding.Spec.RBAC.Create || input.Binding.Spec.ScenarioParameters["deviceId"] != "device-dev-1" {
		t.Fatalf("typed binding inheritance lost: %+v", input.Binding.Spec)
	}
	if input.RepoRoot != host || len(input.Scenario.Spec.Operations) != 2 {
		t.Fatal("trusted runtime root or shared steps lost")
	}
	if _, err := prepared.Plan(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A caller replaces just one matching shared step, not every inherited step.
	shared, err := os.ReadFile(filepath.Join(host, "examples/catalogs/telemetry-steps.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog workspace.StepCatalog
	if err := yaml.Unmarshal(shared, &catalog); err != nil {
		t.Fatal(err)
	}
	step := catalog.Spec.Steps[2]
	step.Output.GraphQLQueries = nil
	step.Output.Operations[0].MQTT.Topic = "caller/${param.tenantId}/{deviceId}/readings"
	catalog.Spec.Steps = []workspace.StepDefinition{step}
	data, err := yaml.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caller, "steps.yaml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	request.Scenario.RuntimeConfig, err = scenario.ParseRuntimeConfig([]byte("[configuration]\nbinding='binding.yaml'\ncatalogs=['steps.yaml']"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err = runtime.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got := prepared.(*preparedMigrationRuntime).inputs[0].Scenario.Spec.Operations[0].MQTT.Topic; !strings.HasPrefix(got, "caller/") {
		t.Fatal("caller catalog did not win", got)
	}
	for _, invalid := range []string{"spec:\n  namespace: other\n", "spec:\n  kubeContext: other\n", "spec:\n  unknown: true\n"} {
		if err := os.WriteFile(filepath.Join(caller, "binding.yaml"), []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.Resolve(context.Background(), request); err == nil {
			t.Fatal("invalid or escaping binding admitted")
		}
	}
}

func TestRepositoryDefaultsRemainBelowCallerOverrides(t *testing.T) {
	host := t.TempDir()
	caller := t.TempDir()
	// Reuse the existing suite as trusted base; defaults live in its host checkout.
	root := repoRoot(t)
	defaultsFile := filepath.Join(host, "defaults.toml")
	if err := os.WriteFile(defaultsFile, []byte("[probe]\nimage='host-default:v1'\n[execution]\nfail_fast=true"), 0600); err != nil {
		t.Fatal(err)
	}
	// The factory deliberately rejects defaults outside its trusted checkout.
	if _, err := NewReceiverMigrationRuntimeWithSources(root, "examples/suites/mqtt-local.yaml", "v1.0.0", ReceiverMigrationSources{Directory: caller, RepositoryConfig: defaultsFile}); err == nil {
		t.Fatal("absolute defaults path admitted")
	}
	runtime, err := NewReceiverMigrationRuntimeWithSources(host, filepath.Join(root, "examples/suites/mqtt-local.yaml"), "v1.0.0", ReceiverMigrationSources{Directory: caller, RepositoryConfig: "defaults.toml"})
	if err != nil {
		t.Fatal(err)
	}
	inline := scenarioSmokeSource
	request := scenarioruntime.ResolveRequest{Workspace: host, Scenario: scenario.Scenario{Schema: scenario.Schema, Runtime: runtime.ID(), Tests: []scenario.TestSource{{Inline: &inline}}}}
	prepared, err := runtime.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	resolved := prepared.(*preparedMigrationRuntime)
	if resolved.inputs[0].Binding.Spec.Probe.Image != "host-default:v1" || !*resolved.resolved.Suite.Spec.Execution.FailFast {
		t.Fatal("repository defaults lost")
	}
	request.Scenario.RuntimeConfig, _ = scenario.ParseRuntimeConfig([]byte("[probe]\nimage='caller:v2'\n[execution]\nfail_fast=false"))
	prepared, err = runtime.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	resolved = prepared.(*preparedMigrationRuntime)
	if resolved.inputs[0].Binding.Spec.Probe.Image != "caller:v2" || *resolved.resolved.Suite.Spec.Execution.FailFast {
		t.Fatal("caller did not override repository defaults")
	}
}

func TestReceiverConfigurationFieldGuardrails(t *testing.T) {
	for _, tc := range []struct {
		config string
		fields []string
		pass   bool
	}{
		{"[selection]\ninclude_tags=['group-baseline']", nil, true},
		{"[configuration]\ncatalogs=['steps.yaml']", nil, false},
		{"[configuration]\ncatalogs=['steps.yaml']", []string{"configuration.catalogs"}, true},
		{"[configuration]\nbinding='binding.yaml'", []string{"configuration.catalogs"}, false},
		{"[execution]\nfail_fast=false", []string{"execution.fail_fast"}, true},
		{"[execution]\nbefore_scenario_hook='evil.sh'", nil, false},
		{"[configuration]\nbinding='../outside.yaml'", []string{"configuration.binding"}, false},
		{"[configuration]\ncatalgos=[]", []string{"configuration.catalogs"}, false},
	} {
		raw, err := scenario.ParseRuntimeConfig([]byte(tc.config))
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateReceiverMigrationConfig(raw, tc.fields); (err == nil) != tc.pass {
			t.Fatalf("%s admission=%v expected=%v", tc.config, err, tc.pass)
		}
	}
	base, _ := scenario.ParseRuntimeConfig([]byte("[configuration]\ncatalogs=['base.yaml']\nbinding='base-binding.yaml'"))
	override, _ := scenario.ParseRuntimeConfig([]byte("[configuration]\ncatalogs=[]"))
	merged, err := (migrationRuntime{}).MergeConfig(base, override)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeMigrationOverlay(merged)
	if err != nil || decoded.Configuration.Catalogs == nil || len(*decoded.Configuration.Catalogs) != 0 || *decoded.Configuration.Binding != "base-binding.yaml" {
		t.Fatal("array replacement or inheritance lost")
	}
	if strings.Contains(string(merged.Bytes()), "base.yaml") {
		t.Fatal("catalog array appended")
	}
}
