package workspace

func NewBuiltInProviderRegistry() (*ProviderRegistry, error) {
	return NewProviderRegistryWithProviders(nil)
}

func NewProviderRegistryWithProviders(providers []Provider) (*ProviderRegistry, error) {
	registry := NewProviderRegistry()
	for _, provider := range builtInProviders() {
		if err := registry.Register(provider); err != nil {
			return nil, err
		}
	}
	for _, provider := range providers {
		if err := registry.Register(provider); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func builtInProviders() []Provider {
	return []Provider{
		builtInProvider("mqtt", "mqtt.connection", []string{
			"mqtt.publish",
			"mqtt.roundtrip",
		}),
		builtInProvider("redpanda", "redpanda.connection", []string{
			"redpanda.contains",
			"redpanda.snapshotOffsets",
		}),
		builtInProvider("graphql", "graphql.endpoint", []string{
			"graphql.expect",
		}),
		builtInProvider("influxdb", "influxdb.connection", []string{
			"influxdb.expect",
		}),
		builtInProvider("mongodb", "mongodb.connection", []string{
			"mongodb.expect",
		}),
		builtInProvider("postgresql", "postgresql.connection", []string{
			"postgresql.expect",
		}),
		builtInProvider("rabbitmq", "rabbitmq.connection", []string{
			"rabbitmq.publish",
			"rabbitmq.expect",
		}),
		builtInProvider("redis", "redis.connection", []string{
			"redis.get",
			"redis.assertKeyExists",
			"redis.assertValueEquals",
		}),
	}
}

func builtInProvider(name, bindingKind string, operationTypes []string) Provider {
	capabilities := make([]Capability, 0, len(operationTypes))
	for _, operationType := range operationTypes {
		capabilities = append(capabilities, Capability{
			Type:         operationType,
			InputSchema:  SchemaRef{Name: operationType + ".input", Schema: builtInInputSchema(operationType)},
			ResultSchema: SchemaRef{Name: operationType + ".result", Schema: builtInResultSchema(operationType)},
			BindingKind:  bindingKind,
			Probe: ProbeInvocationSpec{
				Image:   "spex-probe:dev",
				Command: []string{name, "run"},
				Input:   ProbeIO{Mode: "operationFile", Path: "/spex/input/operation.json"},
				Output:  ProbeIO{Path: "/spex/output/result.json"},
			},
			Validate: builtInInputValidator(operationType),
		})
	}
	return Provider{
		Name:         name,
		Capabilities: capabilities,
		BindingSchemas: []BindingSchema{
			{Kind: bindingKind},
		},
	}
}

func builtInInputSchema(operationType string) *JSONSchema {
	switch operationType {
	case "redis.get":
		return objectSchema([]string{"key"}, map[string]JSONSchema{
			"key": stringSchema(),
		})
	case "redis.assertKeyExists":
		return objectSchema([]string{"key"}, map[string]JSONSchema{
			"key": stringSchema(),
		})
	case "redis.assertValueEquals":
		return objectSchema([]string{"key", "equals"}, map[string]JSONSchema{
			"key":    stringSchema(),
			"equals": stringSchema(),
		})
	case "mqtt.publish":
		return objectSchema([]string{"topic", "payload", "correlationId"}, map[string]JSONSchema{
			"topic":              stringSchema(),
			"payloadTemplateRef": stringSchema(),
			"payload":            stringSchema(),
			"correlationId":      stringSchema(),
			"clientId":           stringSchema(),
		})
	case "mqtt.roundtrip":
		return objectSchema([]string{"topic", "payload", "correlationId", "match"}, map[string]JSONSchema{
			"topic":              stringSchema(),
			"payloadTemplateRef": stringSchema(),
			"payload":            stringSchema(),
			"correlationId":      stringSchema(),
			"clientId":           stringSchema(),
			"match":              arraySchema(objectSchemaValue(nil, nil)),
		})
	case "redpanda.contains":
		return objectSchema([]string{"topic", "offsetsConfigMap", "correlationId", "match"}, map[string]JSONSchema{
			"topicRef":         stringSchema(),
			"topic":            stringSchema(),
			"offsetsConfigMap": stringSchema(),
			"namespace":        stringSchema(),
			"scenario":         stringSchema(),
			"runId":            stringSchema(),
			"correlationId":    stringSchema(),
			"match":            arraySchema(objectSchemaValue(nil, nil)),
		})
	case "redpanda.snapshotOffsets":
		return objectSchema([]string{"topics"}, map[string]JSONSchema{
			"topics":           arraySchema(stringSchema()),
			"offsetsConfigMap": stringSchema(),
			"offsetsFile":      stringSchema(),
			"namespace":        stringSchema(),
			"scenario":         stringSchema(),
			"runId":            stringSchema(),
		})
	case "graphql.expect":
		return objectSchema([]string{"query", "variables", "match"}, map[string]JSONSchema{
			"queryRef":  stringSchema(),
			"query":     stringSchema(),
			"variables": objectSchemaValue(nil, nil),
			"match":     arraySchema(objectSchemaValue(nil, nil)),
		})
	case "influxdb.expect":
		return objectSchema([]string{"query", "match"}, map[string]JSONSchema{
			"query":         stringSchema(),
			"language":      {Type: "string", Enum: []string{"flux", "sql", "influxql"}},
			"correlationId": stringSchema(),
			"match":         arraySchema(objectSchemaValue(nil, nil)),
		})
	case "mongodb.expect":
		return objectSchema([]string{"collection", "filter", "correlationId", "match"}, map[string]JSONSchema{
			"collection":    stringSchema(),
			"filter":        stringSchema(),
			"correlationId": stringSchema(),
			"match":         arraySchema(objectSchemaValue(nil, nil)),
		})
	case "postgresql.expect":
		return objectSchema([]string{"query", "correlationId", "match"}, map[string]JSONSchema{
			"query":         stringSchema(),
			"correlationId": stringSchema(),
			"args":          arraySchema(stringSchema()),
			"match":         arraySchema(objectSchemaValue(nil, nil)),
		})
	case "rabbitmq.publish":
		return objectSchema([]string{"routingKey", "payload", "correlationId"}, map[string]JSONSchema{
			"exchange":           stringSchema(),
			"routingKey":         stringSchema(),
			"payloadTemplateRef": stringSchema(),
			"payload":            stringSchema(),
			"correlationId":      stringSchema(),
		})
	case "rabbitmq.expect":
		return objectSchema([]string{"queue", "correlationId", "match"}, map[string]JSONSchema{
			"queue":         stringSchema(),
			"correlationId": stringSchema(),
			"match":         arraySchema(objectSchemaValue(nil, nil)),
		})
	default:
		return nil
	}
}

func builtInResultSchema(operationType string) *JSONSchema {
	switch operationType {
	case "redis.get":
		return objectSchema([]string{"key", "exists"}, map[string]JSONSchema{
			"key":    stringSchema(),
			"exists": {Type: "boolean"},
			"value":  stringSchema(),
		})
	case "redis.assertKeyExists":
		return objectSchema([]string{"key", "exists"}, map[string]JSONSchema{
			"key":    stringSchema(),
			"exists": {Type: "boolean"},
		})
	case "redis.assertValueEquals":
		return objectSchema([]string{"key", "value"}, map[string]JSONSchema{
			"key":   stringSchema(),
			"value": stringSchema(),
		})
	default:
		return objectSchema(nil, nil)
	}
}

func objectSchema(required []string, properties map[string]JSONSchema) *JSONSchema {
	schema := objectSchemaValue(required, properties)
	return &schema
}

func objectSchemaValue(required []string, properties map[string]JSONSchema) JSONSchema {
	return JSONSchema{Type: "object", Required: required, Properties: properties}
}

func stringSchema() JSONSchema {
	return JSONSchema{Type: "string"}
}

func arraySchema(items JSONSchema) JSONSchema {
	return JSONSchema{Type: "array", Items: &items}
}

func builtInInputValidator(operationType string) OperationInputValidator {
	switch operationType {
	case "mqtt.publish":
		return requireOperationInputStringFields("topic", "payload", "correlationId")
	case "mqtt.roundtrip":
		return requireOperationInputFields("topic", "payload", "correlationId", "match")
	case "redpanda.contains":
		return requireOperationInputFields("topic", "offsetsConfigMap", "correlationId", "match")
	case "redpanda.snapshotOffsets":
		return requireOperationInputFields("topics")
	case "graphql.expect":
		return requireOperationInputFields("query", "variables", "match")
	case "influxdb.expect":
		return requireOperationInputFields("query", "match")
	case "mongodb.expect":
		return requireOperationInputFields("collection", "filter", "correlationId", "match")
	case "postgresql.expect":
		return requireOperationInputFields("query", "correlationId", "match")
	case "rabbitmq.publish":
		return requireOperationInputStringFields("routingKey", "payload", "correlationId")
	case "rabbitmq.expect":
		return requireOperationInputFields("queue", "correlationId", "match")
	case "redis.get":
		return requireOperationInputStringFields("key")
	case "redis.assertKeyExists":
		return requireOperationInputStringFields("key")
	case "redis.assertValueEquals":
		return requireOperationInputStringFields("key", "equals")
	default:
		return nil
	}
}
