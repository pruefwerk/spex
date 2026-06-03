package workspace

import (
	"fmt"
	"reflect"
	"strings"
)

func ValidateOperationInput(operation GenericOperation, capability Capability) error {
	input := operationWithWithoutBindingRef(operation.With)
	if capability.InputSchema.Schema != nil {
		if err := ValidateJSONSchema("with", *capability.InputSchema.Schema, input); err != nil {
			return fmt.Errorf("operation %q %s input schema validation failed: %w", operation.ID, operation.Type, err)
		}
	}
	if capability.Validate != nil {
		if err := capability.Validate(input); err != nil {
			return fmt.Errorf("operation %q %s input validation failed: %w", operation.ID, operation.Type, err)
		}
	}
	return nil
}

func ValidateCapabilityResult(operationID, operationType string, capability Capability, result map[string]any) error {
	if capability.ResultSchema.Schema == nil {
		return nil
	}
	if err := ValidateJSONSchema("result", *capability.ResultSchema.Schema, result); err != nil {
		return fmt.Errorf("operation %q %s result schema validation failed: %w", operationID, operationType, err)
	}
	return nil
}

func ValidateJSONSchema(path string, schema JSONSchema, value any) error {
	if schema.Type != "" {
		if err := validateJSONSchemaType(path, schema.Type, value); err != nil {
			return err
		}
	}
	if len(schema.Enum) > 0 {
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string enum value", path)
		}
		found := false
		for _, allowed := range schema.Enum {
			if text == allowed {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s must be one of %s", path, strings.Join(schema.Enum, ", "))
		}
	}
	if len(schema.Required) > 0 || len(schema.Properties) > 0 {
		object, ok := asStringAnyMap(value)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		for _, field := range schema.Required {
			fieldValue, ok := object[field]
			if !ok || isEmptyOperationInputValue(fieldValue) {
				return fmt.Errorf("%s.%s is required", path, field)
			}
		}
		for field, propertySchema := range schema.Properties {
			fieldValue, ok := object[field]
			if !ok {
				continue
			}
			if err := ValidateJSONSchema(path+"."+field, propertySchema, fieldValue); err != nil {
				return err
			}
		}
	}
	if schema.Items != nil {
		items, ok := asSlice(value)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		for i, item := range items {
			if err := ValidateJSONSchema(fmt.Sprintf("%s[%d]", path, i), *schema.Items, item); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateJSONSchemaType(path, schemaType string, value any) error {
	switch schemaType {
	case "object":
		if _, ok := asStringAnyMap(value); !ok {
			return fmt.Errorf("%s must be an object", path)
		}
	case "array":
		if _, ok := asSlice(value); !ok {
			return fmt.Errorf("%s must be an array", path)
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	case "number":
		if !isNumber(value) {
			return fmt.Errorf("%s must be a number", path)
		}
	case "integer":
		if !isInteger(value) {
			return fmt.Errorf("%s must be an integer", path)
		}
	case "null":
		if value != nil {
			return fmt.Errorf("%s must be null", path)
		}
	default:
		return fmt.Errorf("%s uses unsupported schema type %q", path, schemaType)
	}
	return nil
}

func asStringAnyMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	default:
		rv := reflect.ValueOf(value)
		if !rv.IsValid() {
			return nil, false
		}
		if rv.Kind() == reflect.Struct {
			return map[string]any{}, true
		}
		if rv.Kind() != reflect.Map || rv.Type().Key().Kind() != reflect.String {
			return nil, false
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out[iter.Key().String()] = iter.Value().Interface()
		}
		return out, true
	}
}

func asSlice(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	default:
		rv := reflect.ValueOf(value)
		if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) {
			return nil, false
		}
		out := make([]any, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out = append(out, rv.Index(i).Interface())
		}
		return out, true
	}
}

func isNumber(value any) bool {
	switch value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return true
	default:
		return false
	}
}

func isInteger(value any) bool {
	switch value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}

func requireOperationInputFields(fields ...string) OperationInputValidator {
	return func(input map[string]any) error {
		for _, field := range fields {
			value, ok := input[field]
			if !ok || isEmptyOperationInputValue(value) {
				return fmt.Errorf("with.%s is required", field)
			}
		}
		return nil
	}
}

func requireOperationInputStringFields(fields ...string) OperationInputValidator {
	return func(input map[string]any) error {
		for _, field := range fields {
			value, ok := input[field]
			if !ok {
				return fmt.Errorf("with.%s is required", field)
			}
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("with.%s must be a string", field)
			}
			if text == "" {
				return fmt.Errorf("with.%s is required", field)
			}
		}
		return nil
	}
}

func isEmptyOperationInputValue(value any) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case string:
		return typed == ""
	case []string:
		return len(typed) == 0
	case []Matcher:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	case map[string]string:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}
