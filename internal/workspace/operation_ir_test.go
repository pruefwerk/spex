package workspace

import (
	"strings"
	"testing"
)

func TestNormalizeLegacyOperationsMapsTypedPostgreSQLOperation(t *testing.T) {
	inputs := Inputs{
		Scenario: Scenario{
			Spec: ScenarioSpec{
				Operations: []Operation{
					{
						ID:    "assert-user-row",
						Type:  "postgresql.expect",
						After: "create-user",
						Postgres: &PostgreSQLExpectation{
							Query:         "select id from users where id = $1",
							Args:          []string{"user-123"},
							CorrelationID: "user-123",
							Timeout:       "5s",
							Match: []Matcher{
								{Path: "$.id", EqualsString: "user-123"},
							},
						},
					},
				},
			},
		},
	}

	operations := NormalizeLegacyOperations(inputs)
	if len(operations) != 1 {
		t.Fatalf("expected one operation, got %d", len(operations))
	}
	operation := operations[0]
	if operation.ID != "assert-user-row" || operation.Type != "postgresql.expect" {
		t.Fatalf("unexpected operation identity: %#v", operation)
	}
	if operation.Timeout != "5s" {
		t.Fatalf("unexpected timeout: %q", operation.Timeout)
	}
	if len(operation.DependsOn) != 1 || operation.DependsOn[0] != "create-user" {
		t.Fatalf("unexpected dependsOn: %#v", operation.DependsOn)
	}
	if operation.With[bindingRefKey] != "postgresql.default" {
		t.Fatalf("unexpected bindingRef: %#v", operation.With)
	}
	if operation.With["query"] != "select id from users where id = $1" {
		t.Fatalf("query not copied: %#v", operation.With)
	}
	args, ok := operation.With["args"].([]string)
	if !ok || len(args) != 1 || args[0] != "user-123" {
		t.Fatalf("args not copied: %#v", operation.With["args"])
	}
}

func TestLowerOperationResolvesProviderAndBinding(t *testing.T) {
	registry, err := NewBuiltInProviderRegistry()
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]GenericBinding{
		"redis.main": {
			Name: "redis.main",
			Kind: "redis.connection",
			With: map[string]any{
				"uri":            "redis://redis.default.svc:6379",
				"credentialsRef": "redis-credentials",
			},
		},
	}
	lowered, err := LowerOperation(GenericOperation{
		ID:   "assert-cache-value",
		Type: "redis.assertValueEquals",
		With: map[string]any{
			bindingRefKey: "redis.main",
			"key":         "user-123",
			"equals":      "ready",
		},
	}, bindings, registry, "30s")
	if err != nil {
		t.Fatal(err)
	}
	if lowered.OperationID != "assert-cache-value" || lowered.OperationType != "redis.assertValueEquals" || lowered.Provider != "redis" {
		t.Fatalf("unexpected lowered operation identity: %#v", lowered)
	}
	if lowered.Binding.Name != "redis.main" || lowered.Binding.Kind != "redis.connection" {
		t.Fatalf("unexpected lowered binding: %#v", lowered.Binding)
	}
	if lowered.Binding.With["uri"] != "redis://redis.default.svc:6379" {
		t.Fatalf("binding payload not copied: %#v", lowered.Binding.With)
	}
	if _, exists := lowered.With[bindingRefKey]; exists {
		t.Fatalf("lowered operation with should not retain bindingRef: %#v", lowered.With)
	}
	if lowered.With["key"] != "user-123" || lowered.With["equals"] != "ready" {
		t.Fatalf("operation payload not copied: %#v", lowered.With)
	}
	if lowered.Timeout != "30s" {
		t.Fatalf("unexpected timeout: %q", lowered.Timeout)
	}
}

func TestLowerOperationValidatesProviderInput(t *testing.T) {
	registry, err := NewBuiltInProviderRegistry()
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]GenericBinding{
		"postgresql.default": {
			Name: "postgresql.default",
			Kind: "postgresql.connection",
			With: map[string]any{"uri": "postgresql://postgres.default.svc:5432/app"},
		},
	}
	_, err = LowerOperation(GenericOperation{
		ID:   "assert-user-row",
		Type: "postgresql.expect",
		With: map[string]any{
			bindingRefKey:   "postgresql.default",
			"correlationId": "user-123",
			"match":         []Matcher{{Path: "$.id", EqualsString: "user-123"}},
		},
	}, bindings, registry, "30s")
	if err == nil || !strings.Contains(err.Error(), `operation "assert-user-row" postgresql.expect input schema validation failed: with.query is required`) {
		t.Fatalf("expected provider input validation error, got %v", err)
	}
}

func TestValidateOperationInputUsesProviderSchemaTypes(t *testing.T) {
	registry, err := NewBuiltInProviderRegistry()
	if err != nil {
		t.Fatal(err)
	}
	capability, ok := registry.ResolveCapability("redis.assertValueEquals")
	if !ok {
		t.Fatal("redis.assertValueEquals capability not registered")
	}
	err = ValidateOperationInput(GenericOperation{
		ID:   "assert-cache-value",
		Type: "redis.assertValueEquals",
		With: map[string]any{
			"key":    "cache:user-123",
			"equals": 42,
		},
	}, capability.Capability)
	if err == nil || !strings.Contains(err.Error(), "with.equals must be a string") {
		t.Fatalf("expected schema type validation error, got %v", err)
	}
}

func TestValidateCapabilityResultUsesProviderSchema(t *testing.T) {
	registry, err := NewBuiltInProviderRegistry()
	if err != nil {
		t.Fatal(err)
	}
	capability, ok := registry.ResolveCapability("redis.assertValueEquals")
	if !ok {
		t.Fatal("redis.assertValueEquals capability not registered")
	}
	if err := ValidateCapabilityResult("assert-cache-value", "redis.assertValueEquals", capability.Capability, map[string]any{
		"key":   "cache:user-123",
		"value": "active",
	}); err != nil {
		t.Fatal(err)
	}
	err = ValidateCapabilityResult("assert-cache-value", "redis.assertValueEquals", capability.Capability, map[string]any{
		"key": "cache:user-123",
	})
	if err == nil || !strings.Contains(err.Error(), "result.value is required") {
		t.Fatalf("expected result schema validation error, got %v", err)
	}
}

func TestLowerOperationsBuildsLegacyLoweredOperation(t *testing.T) {
	registry, err := NewBuiltInProviderRegistry()
	if err != nil {
		t.Fatal(err)
	}
	inputs := Inputs{
		Scenario: Scenario{
			Spec: ScenarioSpec{
				Defaults: Defaults{Timeout: "45s"},
				Operations: []Operation{
					{
						ID:   "assert-user-row",
						Type: "postgresql.expect",
						Postgres: &PostgreSQLExpectation{
							Query:         "select id from users where id = $1",
							Args:          []string{"user-123"},
							CorrelationID: "user-123",
							Match: []Matcher{
								{Path: "$.id", EqualsString: "user-123"},
							},
						},
					},
				},
			},
		},
		Binding: TargetBinding{
			Spec: BindingSpec{
				PostgreSQL: PostgreSQLBinding{
					URI:            "postgresql://postgres.default.svc:5432/app",
					CredentialsRef: "postgres-credentials",
				},
			},
		},
	}
	lowered, err := LowerOperations(inputs, registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(lowered) != 1 {
		t.Fatalf("expected one lowered operation, got %d", len(lowered))
	}
	operation := lowered[0]
	if operation.Binding.Name != "postgresql.default" || operation.Binding.Kind != "postgresql.connection" {
		t.Fatalf("unexpected binding: %#v", operation.Binding)
	}
	if operation.Binding.With["uri"] != "postgresql://postgres.default.svc:5432/app" {
		t.Fatalf("legacy binding URI not copied: %#v", operation.Binding.With)
	}
	if operation.Timeout != "45s" {
		t.Fatalf("expected default timeout, got %q", operation.Timeout)
	}
}
