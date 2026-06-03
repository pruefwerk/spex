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
