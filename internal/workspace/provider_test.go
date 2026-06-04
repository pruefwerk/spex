package workspace

import (
	"strings"
	"testing"
)

func TestProviderRegistryRegistersAndResolvesCapabilities(t *testing.T) {
	registry := NewProviderRegistry()
	provider := Provider{
		Name: "redis",
		Capabilities: []Capability{
			{
				Type:        "redis.assertValueEquals",
				BindingKind: "redis.connection",
				Probe: ProbeInvocationSpec{
					Image:   "ghcr.io/pruefwerk/spex-probe-redis@sha256:abc",
					Command: []string{"spex-probe-redis", "run"},
					Input:   ProbeIO{Mode: "operationFile", Path: "/spex/input/operation.json"},
					Output:  ProbeIO{Path: "/spex/output/result.json"},
				},
			},
		},
		BindingSchemas: []BindingSchema{
			{Kind: "redis.connection", Schema: SchemaRef{Path: "schemas/redis-connection.schema.json"}},
		},
	}

	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	capability, ok := registry.ResolveCapability("redis.assertValueEquals")
	if !ok {
		t.Fatal("capability not registered")
	}
	if capability.Provider != "redis" || capability.Capability.BindingKind != "redis.connection" {
		t.Fatalf("unexpected capability registration: %#v", capability)
	}
	bindingSchema, ok := registry.ResolveBindingSchema("redis.connection")
	if !ok {
		t.Fatal("binding schema not registered")
	}
	if bindingSchema.Provider != "redis" {
		t.Fatalf("unexpected binding schema registration: %#v", bindingSchema)
	}
}

func TestProviderRegistryRejectsDuplicateOperationTypesWithinProvider(t *testing.T) {
	registry := NewProviderRegistry()
	err := registry.Register(Provider{
		Name: "redis",
		Capabilities: []Capability{
			testCapability("redis.get", "redis.connection"),
			testCapability("redis.get", "redis.connection"),
		},
		BindingSchemas: []BindingSchema{
			{Kind: "redis.connection"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), `provider "redis" registers duplicate operation type "redis.get"`) {
		t.Fatalf("expected duplicate operation type error, got %v", err)
	}
}

func TestProviderRegistryRejectsUnqualifiedCapabilityTypes(t *testing.T) {
	registry := NewProviderRegistry()
	err := registry.Register(testProvider("redis", "get", "redis.connection"))
	if err == nil || !strings.Contains(err.Error(), "must be fully qualified") {
		t.Fatalf("expected unqualified type error, got %v", err)
	}
}

func TestProviderRegistryRejectsCapabilityWithWrongProviderPrefix(t *testing.T) {
	registry := NewProviderRegistry()
	err := registry.Register(testProvider("redis", "postgresql.queryOne", "redis.connection"))
	if err == nil || !strings.Contains(err.Error(), `must use provider prefix "redis."`) {
		t.Fatalf("expected provider prefix error, got %v", err)
	}
}

func TestProviderRegistryRejectsInvalidProbeEnv(t *testing.T) {
	tests := []struct {
		name    string
		envName string
		source  ProbeEnvSource
		want    string
	}{
		{
			name:    "invalid env name",
			envName: "custom-token",
			source:  ProbeEnvSource{SecretRef: "credentials.token"},
			want:    "must be a valid environment variable name",
		},
		{
			name:    "missing source",
			envName: "CUSTOM_TOKEN",
			source:  ProbeEnvSource{},
			want:    "must declare exactly one source",
		},
		{
			name:    "multiple sources",
			envName: "CUSTOM_TOKEN",
			source:  ProbeEnvSource{Value: "token", SecretRef: "credentials.token"},
			want:    "must declare exactly one source",
		},
		{
			name:    "invalid from binding path",
			envName: "CUSTOM_URI",
			source:  ProbeEnvSource{FromBinding: "credentials-ref"},
			want:    "must be a dotted binding path",
		},
		{
			name:    "invalid secret ref",
			envName: "CUSTOM_TOKEN",
			source:  ProbeEnvSource{SecretRef: "credentials"},
			want:    "must be a dotted secret reference",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewProviderRegistry()
			provider := testProvider("custom", "custom.echo", "custom.connection")
			provider.Capabilities[0].Probe.Env = map[string]ProbeEnvSource{
				tt.envName: tt.source,
			}
			err := registry.Register(provider)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q error, got %v", tt.want, err)
			}
		})
	}
}

func TestBuiltInProviderRegistryResolvesCurrentOperationTypes(t *testing.T) {
	registry, err := NewBuiltInProviderRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, operationType := range []string{
		"mqtt.publish",
		"mqtt.roundtrip",
		"redpanda.contains",
		"redpanda.snapshotOffsets",
		"graphql.expect",
		"influxdb.expect",
		"mongodb.expect",
		"postgresql.expect",
		"rabbitmq.publish",
		"rabbitmq.expect",
		"redis.get",
		"redis.assertKeyExists",
		"redis.assertValueEquals",
	} {
		capability, ok := registry.ResolveCapability(operationType)
		if !ok {
			t.Fatalf("operation type %q not registered", operationType)
		}
		if capability.Provider == "" {
			t.Fatalf("operation type %q has empty provider", operationType)
		}
		if capability.Capability.InputSchema.Schema == nil || capability.Capability.ResultSchema.Schema == nil {
			t.Fatalf("operation type %q missing provider schemas", operationType)
		}
	}
}

func TestBuiltInProviderLookup(t *testing.T) {
	provider, ok := BuiltInProvider("redis")
	if !ok {
		t.Fatal("redis built-in provider not found")
	}
	if provider.Name != "redis" || len(provider.Capabilities) == 0 {
		t.Fatalf("unexpected redis provider: %#v", provider)
	}
	if _, ok := BuiltInProvider("does-not-exist"); ok {
		t.Fatal("unexpected unknown built-in provider")
	}
}

func testProvider(name, operationType, bindingKind string) Provider {
	return Provider{
		Name: name,
		Capabilities: []Capability{
			testCapability(operationType, bindingKind),
		},
		BindingSchemas: []BindingSchema{
			{Kind: bindingKind},
		},
	}
}

func testCapability(operationType, bindingKind string) Capability {
	return Capability{
		Type:        operationType,
		BindingKind: bindingKind,
		Probe: ProbeInvocationSpec{
			Image:   "ghcr.io/pruefwerk/spex-probe-test@sha256:abc",
			Command: []string{"spex-probe-test", "run"},
		},
	}
}
