package workspace

import (
	"encoding/json"
	"fmt"
)

const bindingRefKey = "bindingRef"

func NormalizeLegacyOperations(in Inputs) []GenericOperation {
	out := make([]GenericOperation, 0, len(in.Scenario.Spec.Operations))
	for _, op := range in.Scenario.Spec.Operations {
		generic := GenericOperation{
			ID:        op.ID,
			Type:      op.Type,
			With:      copyAnyMap(op.With),
			Timeout:   legacyOperationTimeout(op),
			DependsOn: legacyDependsOn(op),
		}
		if generic.With == nil {
			generic.With = map[string]any{}
		}
		if _, ok := generic.With[bindingRefKey]; !ok {
			generic.With[bindingRefKey] = legacyBindingName(providerNameForOperationType(op.Type))
		}
		switch op.Type {
		case "mqtt.publish", "mqtt.roundtrip":
			if op.MQTT != nil {
				generic.With["topic"] = renderTemplate(op.MQTT.Topic, in.RunID, op.MQTT.CorrelationID, resolvedParameters(in))
				generic.With["payloadTemplateRef"] = op.MQTT.PayloadTemplateRef
				generic.With["correlationId"] = op.MQTT.CorrelationID
				generic.With["clientMode"] = op.MQTT.ClientMode
				generic.With["clientId"] = legacyMQTTClientID(in, op)
				if op.MQTT.PayloadTemplateRef != "" {
					template := in.Scenario.Spec.PayloadTemplates[op.MQTT.PayloadTemplateRef]
					generic.With["payload"] = renderJSONTemplate(template.Body, in.RunID, op.MQTT.CorrelationID, resolvedParameters(in))
				}
				generic.With["match"] = decodeJSONArray(renderMatchersJSON(op.MQTT.Match, in.RunID, op.MQTT.CorrelationID, resolvedParameters(in)))
			}
		case "redpanda.contains":
			if op.Redpanda != nil {
				generic.With["topicRef"] = op.Redpanda.TopicRef
				generic.With["topic"] = redpandaTopicName(in, op.Redpanda.TopicRef)
				generic.With["correlationId"] = op.Redpanda.CorrelationID
				generic.With["fromBeginning"] = op.Redpanda.FromBeginning
				generic.With["offsetsConfigMap"] = offsetConfigMapName(DNSLabel(in.ScenarioName))
				generic.With["namespace"] = in.Namespace
				generic.With["scenario"] = DNSLabel(in.ScenarioName)
				generic.With["runId"] = in.RunID
				generic.With["match"] = decodeJSONArray(renderMatchersJSON(op.Redpanda.Match, in.RunID, op.Redpanda.CorrelationID, resolvedParameters(in)))
			}
		case "graphql.expect":
			if op.GraphQL != nil {
				query := in.Scenario.Spec.GraphQLQueries[op.GraphQL.QueryRef]
				correlationID := graphqlCorrelationID(op)
				generic.With["queryRef"] = op.GraphQL.QueryRef
				generic.With["query"] = readQueryBody(in, query.File)
				generic.With["variables"] = decodeJSONMap(renderStringMapJSON(op.GraphQL.Variables, in.RunID, correlationID, resolvedParameters(in)))
				generic.With["match"] = decodeJSONArray(renderMatchersJSON(op.GraphQL.Match, in.RunID, correlationID, resolvedParameters(in)))
			}
		case "mongodb.expect":
			if op.MongoDB != nil {
				generic.With["collection"] = op.MongoDB.Collection
				generic.With["filter"] = renderJSONTemplate(op.MongoDB.Filter, in.RunID, op.MongoDB.CorrelationID, resolvedParameters(in))
				generic.With["correlationId"] = op.MongoDB.CorrelationID
				generic.With["scenarioScoped"] = scenarioScoped(op.MongoDB.ScenarioScoped)
				generic.With["match"] = decodeJSONArray(renderMatchersJSON(op.MongoDB.Match, in.RunID, op.MongoDB.CorrelationID, resolvedParameters(in)))
			}
		case "postgresql.expect":
			if op.Postgres != nil {
				generic.With["query"] = op.Postgres.Query
				generic.With["args"] = op.Postgres.Args
				generic.With["correlationId"] = op.Postgres.CorrelationID
				generic.With["match"] = op.Postgres.Match
			}
		case "rabbitmq.publish", "rabbitmq.expect":
			if op.RabbitMQ != nil {
				generic.With["exchange"] = renderTemplate(op.RabbitMQ.Exchange, in.RunID, op.RabbitMQ.CorrelationID, resolvedParameters(in))
				generic.With["routingKey"] = renderTemplate(op.RabbitMQ.RoutingKey, in.RunID, op.RabbitMQ.CorrelationID, resolvedParameters(in))
				generic.With["queue"] = renderTemplate(op.RabbitMQ.Queue, in.RunID, op.RabbitMQ.CorrelationID, resolvedParameters(in))
				generic.With["payloadTemplateRef"] = op.RabbitMQ.PayloadTemplateRef
				generic.With["correlationId"] = op.RabbitMQ.CorrelationID
				if op.RabbitMQ.PayloadTemplateRef != "" {
					template := in.Scenario.Spec.PayloadTemplates[op.RabbitMQ.PayloadTemplateRef]
					generic.With["payload"] = renderJSONTemplate(template.Body, in.RunID, op.RabbitMQ.CorrelationID, resolvedParameters(in))
				}
				generic.With["match"] = decodeJSONArray(renderMatchersJSON(op.RabbitMQ.Match, in.RunID, op.RabbitMQ.CorrelationID, resolvedParameters(in)))
			}
		}
		out = append(out, generic)
	}
	return out
}

func decodeJSONMap(content string) map[string]any {
	var out map[string]any
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return map[string]any{}
	}
	return out
}

func decodeJSONArray(content string) []any {
	var out []any
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return nil
	}
	return out
}

func ResolveGenericBindings(binding TargetBinding) (map[string]GenericBinding, error) {
	out := map[string]GenericBinding{}
	for _, generic := range legacyGenericBindings(binding) {
		if err := addGenericBinding(out, generic); err != nil {
			return nil, err
		}
	}
	for _, generic := range binding.Spec.Bindings {
		if err := addGenericBinding(out, generic); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func LowerOperations(in Inputs, registry *ProviderRegistry) ([]LoweredOperation, error) {
	bindings, err := ResolveGenericBindings(in.Binding)
	if err != nil {
		return nil, err
	}
	genericOperations := NormalizeLegacyOperations(in)
	lowered := make([]LoweredOperation, 0, len(genericOperations))
	for _, generic := range genericOperations {
		operation, err := LowerOperation(generic, bindings, registry, defaultTimeout(in))
		if err != nil {
			return nil, err
		}
		lowered = append(lowered, operation)
	}
	return lowered, nil
}

func LowerOperation(operation GenericOperation, bindings map[string]GenericBinding, registry *ProviderRegistry, fallbackTimeout string) (LoweredOperation, error) {
	capability, ok := registry.ResolveCapability(operation.Type)
	if !ok {
		return LoweredOperation{}, fmt.Errorf("operation %q uses unregistered operation type %q", operation.ID, operation.Type)
	}
	if err := ValidateOperationInput(operation, capability.Capability); err != nil {
		return LoweredOperation{}, err
	}
	bindingRef, _ := operation.With[bindingRefKey].(string)
	if bindingRef == "" {
		bindingRef = legacyBindingName(capability.Provider)
	}
	binding, ok := bindings[bindingRef]
	if !ok {
		return LoweredOperation{}, fmt.Errorf("operation %q references unknown binding %q", operation.ID, bindingRef)
	}
	if binding.Kind != capability.Capability.BindingKind {
		return LoweredOperation{}, fmt.Errorf("operation %q binding %q has kind %q, expected %q", operation.ID, bindingRef, binding.Kind, capability.Capability.BindingKind)
	}
	timeout := operation.Timeout
	if timeout == "" {
		timeout = fallbackTimeout
	}
	dependsOn := append([]string(nil), operation.DependsOn...)
	if dependsOn == nil {
		dependsOn = []string{}
	}
	return LoweredOperation{
		OperationID:   operation.ID,
		OperationType: operation.Type,
		Provider:      capability.Provider,
		Binding: LoweredBinding{
			Name: binding.Name,
			Kind: binding.Kind,
			With: copyAnyMap(binding.With),
		},
		With:      operationWithWithoutBindingRef(operation.With),
		Timeout:   timeout,
		DependsOn: dependsOn,
	}, nil
}

func addGenericBinding(bindings map[string]GenericBinding, binding GenericBinding) error {
	if binding.Name == "" {
		return fmt.Errorf("generic binding name is required")
	}
	if binding.Kind == "" {
		return fmt.Errorf("generic binding %q kind is required", binding.Name)
	}
	if _, exists := bindings[binding.Name]; exists {
		return fmt.Errorf("generic binding %q is already defined", binding.Name)
	}
	bindings[binding.Name] = binding
	return nil
}

func legacyGenericBindings(binding TargetBinding) []GenericBinding {
	return []GenericBinding{
		legacyGenericBinding("mqtt", "mqtt.connection", map[string]any{
			"brokerURL":      binding.Spec.MQTT.BrokerURL,
			"clientIdPrefix": binding.Spec.MQTT.ClientIDPrefix,
			"credentialsRef": binding.Spec.MQTT.CredentialsRef,
		}),
		legacyGenericBinding("redpanda", "redpanda.connection", map[string]any{
			"brokers":          binding.Spec.Redpanda.Brokers,
			"securityProtocol": binding.Spec.Redpanda.SecurityProtocol,
			"saslMechanism":    binding.Spec.Redpanda.SASLMechanism,
			"credentialsRef":   binding.Spec.Redpanda.CredentialsRef,
			"caCertRef":        binding.Spec.Redpanda.CACertRef,
			"topics":           binding.Spec.Redpanda.Topics,
		}),
		legacyGenericBinding("keycloak", "keycloak.realm", map[string]any{
			"tokenURL":       binding.Spec.Keycloak.TokenURL,
			"clientID":       binding.Spec.Keycloak.ClientID,
			"credentialsRef": binding.Spec.Keycloak.CredentialsRef,
			"scopes":         append([]string(nil), binding.Spec.Keycloak.Scopes...),
		}),
		legacyGenericBinding("graphql", "graphql.endpoint", map[string]any{
			"endpoint":       binding.Spec.GraphQL.Endpoint,
			"credentialsRef": binding.Spec.GraphQL.CredentialsRef,
			"auth":           legacyGraphQLAuth(binding.Spec),
		}),
		legacyGenericBinding("mongodb", "mongodb.connection", map[string]any{
			"deployment":     binding.Spec.MongoDB.Deployment,
			"uri":            binding.Spec.MongoDB.URI,
			"database":       binding.Spec.MongoDB.Database,
			"credentialsRef": binding.Spec.MongoDB.CredentialsRef,
		}),
		legacyGenericBinding("postgresql", "postgresql.connection", map[string]any{
			"uri":            binding.Spec.PostgreSQL.URI,
			"credentialsRef": binding.Spec.PostgreSQL.CredentialsRef,
		}),
		legacyGenericBinding("rabbitmq", "rabbitmq.connection", map[string]any{
			"uri":            binding.Spec.RabbitMQ.URI,
			"credentialsRef": binding.Spec.RabbitMQ.CredentialsRef,
		}),
	}
}

func legacyGraphQLAuth(binding BindingSpec) map[string]any {
	auth := binding.GraphQL.Auth
	if auth.Type == "keycloakClientCredentials" && auth.KeycloakRef != "" {
		ref := auth.KeycloakRef
		if ref == legacyBindingName("keycloak") || ref == "keycloak" {
			return map[string]any{
				"type":            auth.Type,
				"tokenURL":        binding.Keycloak.TokenURL,
				"clientID":        binding.Keycloak.ClientID,
				"clientSecretRef": binding.Keycloak.CredentialsRef,
				"keycloakRef":     ref,
				"scopes":          append([]string(nil), binding.Keycloak.Scopes...),
			}
		}
	}
	return map[string]any{
		"type":            auth.Type,
		"tokenURL":        auth.TokenURL,
		"clientID":        auth.ClientID,
		"clientSecretRef": auth.ClientSecretRef,
		"keycloakRef":     auth.KeycloakRef,
		"scopes":          append([]string(nil), auth.Scopes...),
	}
}

func legacyGenericBinding(provider, kind string, with map[string]any) GenericBinding {
	return GenericBinding{
		Name: legacyBindingName(provider),
		Kind: kind,
		With: with,
	}
}

func legacyMQTTClientID(in Inputs, op Operation) string {
	prefix := in.Binding.Spec.MQTT.ClientIDPrefix
	if prefix == "" {
		prefix = "spex"
	}
	return DNSLabel(prefix + "-" + in.ScenarioName + "-" + in.RunID + "-" + op.ID)
}

func legacyBindingName(provider string) string {
	return provider + ".default"
}

func providerNameForOperationType(operationType string) string {
	for i, ch := range operationType {
		if ch == '.' {
			return operationType[:i]
		}
	}
	return operationType
}

func legacyOperationTimeout(op Operation) string {
	if op.Timeout != "" {
		return op.Timeout
	}
	switch {
	case op.MQTT != nil:
		return op.MQTT.Timeout
	case op.Redpanda != nil:
		return op.Redpanda.Timeout
	case op.GraphQL != nil:
		return op.GraphQL.Timeout
	case op.MongoDB != nil:
		return op.MongoDB.Timeout
	case op.Postgres != nil:
		return op.Postgres.Timeout
	case op.RabbitMQ != nil:
		return op.RabbitMQ.Timeout
	default:
		return ""
	}
}

func legacyDependsOn(op Operation) []string {
	if len(op.DependsOn) > 0 {
		return append([]string(nil), op.DependsOn...)
	}
	if op.After == "" {
		return nil
	}
	return []string{op.After}
}

func operationWithWithoutBindingRef(with map[string]any) map[string]any {
	out := copyAnyMap(with)
	delete(out, bindingRefKey)
	return out
}

func copyAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
