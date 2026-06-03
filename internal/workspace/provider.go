package workspace

import (
	"fmt"
	"regexp"
)

var operationTypePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*\.[A-Za-z][A-Za-z0-9_.-]*$`)

type GenericOperation struct {
	ID        string         `json:"id" yaml:"id"`
	Type      string         `json:"type" yaml:"type"`
	With      map[string]any `json:"with" yaml:"with"`
	Timeout   string         `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	DependsOn []string       `json:"dependsOn,omitempty" yaml:"dependsOn,omitempty"`
}

type GenericBinding struct {
	Name string         `json:"name" yaml:"name"`
	Kind string         `json:"kind" yaml:"kind"`
	With map[string]any `json:"with" yaml:"with"`
}

type LoweredOperation struct {
	OperationID   string                 `json:"operationId"`
	OperationType string                 `json:"operationType"`
	Provider      string                 `json:"provider"`
	Binding       LoweredBinding         `json:"binding"`
	With          map[string]any         `json:"with"`
	Timeout       string                 `json:"timeout"`
	DependsOn     []string               `json:"dependsOn"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

type LoweredBinding struct {
	Name string         `json:"name"`
	Kind string         `json:"kind"`
	With map[string]any `json:"with"`
}

type ResultEnvelope struct {
	OperationID   string             `json:"operationId"`
	OperationType string             `json:"operationType"`
	Provider      string             `json:"provider"`
	Status        string             `json:"status"`
	Result        map[string]any     `json:"result"`
	Evidence      []EvidenceEnvelope `json:"evidence"`
	Diagnostics   []Diagnostic       `json:"diagnostics"`
}

type EvidenceEnvelope struct {
	Kind string `json:"kind"`
	Path string `json:"path,omitempty"`
	Ref  string `json:"ref,omitempty"`
}

type Diagnostic struct {
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message"`
}

type SchemaRef struct {
	Name   string      `json:"name,omitempty" yaml:"name,omitempty"`
	Path   string      `json:"path,omitempty" yaml:"path,omitempty"`
	Schema *JSONSchema `json:"schema,omitempty" yaml:"schema,omitempty"`
}

type JSONSchema struct {
	Type                 string                `json:"type,omitempty" yaml:"type,omitempty"`
	Required             []string              `json:"required,omitempty" yaml:"required,omitempty"`
	Properties           map[string]JSONSchema `json:"properties,omitempty" yaml:"properties,omitempty"`
	Items                *JSONSchema           `json:"items,omitempty" yaml:"items,omitempty"`
	Enum                 []string              `json:"enum,omitempty" yaml:"enum,omitempty"`
	AdditionalProperties *bool                 `json:"additionalProperties,omitempty" yaml:"additionalProperties,omitempty"`
}

type Capability struct {
	Type         string              `json:"type" yaml:"type"`
	InputSchema  SchemaRef           `json:"inputSchema,omitempty" yaml:"inputSchema,omitempty"`
	ResultSchema SchemaRef           `json:"resultSchema,omitempty" yaml:"resultSchema,omitempty"`
	BindingKind  string              `json:"bindingKind" yaml:"bindingKind"`
	Probe        ProbeInvocationSpec `json:"probe" yaml:"probe"`
	Validate     OperationInputValidator
}

type OperationInputValidator func(map[string]any) error

type BindingSchema struct {
	Kind   string    `json:"kind" yaml:"kind"`
	Schema SchemaRef `json:"schema,omitempty" yaml:"schema,omitempty"`
}

type Provider struct {
	Name           string
	Capabilities   []Capability
	BindingSchemas []BindingSchema
}

type ProbeInvocationSpec struct {
	Image   string                    `json:"image,omitempty" yaml:"image,omitempty"`
	Command []string                  `json:"command,omitempty" yaml:"command,omitempty"`
	Args    []string                  `json:"args,omitempty" yaml:"args,omitempty"`
	Input   ProbeIO                   `json:"input,omitempty" yaml:"input,omitempty"`
	Output  ProbeIO                   `json:"output,omitempty" yaml:"output,omitempty"`
	Env     map[string]ProbeEnvSource `json:"env,omitempty" yaml:"env,omitempty"`
}

type ProbeIO struct {
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
}

type ProbeEnvSource struct {
	FromBinding string `json:"fromBinding,omitempty" yaml:"fromBinding,omitempty"`
	SecretRef   string `json:"secretRef,omitempty" yaml:"secretRef,omitempty"`
	Value       string `json:"value,omitempty" yaml:"value,omitempty"`
}

type ProviderRegistry struct {
	providers      map[string]Provider
	capabilities   map[string]CapabilityRegistration
	bindingSchemas map[string]BindingSchemaRegistration
}

type CapabilityRegistration struct {
	Provider   string
	Capability Capability
}

type BindingSchemaRegistration struct {
	Provider string
	Schema   BindingSchema
}

func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		providers:      map[string]Provider{},
		capabilities:   map[string]CapabilityRegistration{},
		bindingSchemas: map[string]BindingSchemaRegistration{},
	}
}

func (r *ProviderRegistry) Register(provider Provider) error {
	if provider.Name == "" {
		return fmt.Errorf("provider name is required")
	}
	if !idPattern.MatchString(provider.Name) {
		return fmt.Errorf("provider name %q must match %s", provider.Name, idPattern.String())
	}
	if _, exists := r.providers[provider.Name]; exists {
		return fmt.Errorf("provider %q is already registered", provider.Name)
	}
	seenCapabilities := map[string]struct{}{}
	for _, capability := range provider.Capabilities {
		if err := validateCapability(provider.Name, capability); err != nil {
			return err
		}
		if _, exists := seenCapabilities[capability.Type]; exists {
			return fmt.Errorf("provider %q registers duplicate operation type %q", provider.Name, capability.Type)
		}
		seenCapabilities[capability.Type] = struct{}{}
		if previous, exists := r.capabilities[capability.Type]; exists {
			return fmt.Errorf("operation type %q is already provided by %q", capability.Type, previous.Provider)
		}
	}
	seenBindingSchemas := map[string]struct{}{}
	for _, bindingSchema := range provider.BindingSchemas {
		if err := validateBindingSchema(provider.Name, bindingSchema); err != nil {
			return err
		}
		if _, exists := seenBindingSchemas[bindingSchema.Kind]; exists {
			return fmt.Errorf("provider %q registers duplicate binding kind %q", provider.Name, bindingSchema.Kind)
		}
		seenBindingSchemas[bindingSchema.Kind] = struct{}{}
		if previous, exists := r.bindingSchemas[bindingSchema.Kind]; exists {
			return fmt.Errorf("binding kind %q is already provided by %q", bindingSchema.Kind, previous.Provider)
		}
	}
	r.providers[provider.Name] = provider
	for _, capability := range provider.Capabilities {
		r.capabilities[capability.Type] = CapabilityRegistration{
			Provider:   provider.Name,
			Capability: capability,
		}
	}
	for _, bindingSchema := range provider.BindingSchemas {
		r.bindingSchemas[bindingSchema.Kind] = BindingSchemaRegistration{
			Provider: provider.Name,
			Schema:   bindingSchema,
		}
	}
	return nil
}

func (r *ProviderRegistry) ResolveCapability(operationType string) (CapabilityRegistration, bool) {
	capability, ok := r.capabilities[operationType]
	return capability, ok
}

func (r *ProviderRegistry) ResolveBindingSchema(kind string) (BindingSchemaRegistration, bool) {
	schema, ok := r.bindingSchemas[kind]
	return schema, ok
}

func validateCapability(providerName string, capability Capability) error {
	if !operationTypePattern.MatchString(capability.Type) {
		return fmt.Errorf("provider %q capability type %q must be fully qualified as provider.operation", providerName, capability.Type)
	}
	prefix := providerName + "."
	if len(capability.Type) <= len(prefix) || capability.Type[:len(prefix)] != prefix {
		return fmt.Errorf("provider %q capability type %q must use provider prefix %q", providerName, capability.Type, prefix)
	}
	if capability.BindingKind == "" {
		return fmt.Errorf("provider %q capability %q requires binding kind", providerName, capability.Type)
	}
	if capability.Probe.Image == "" {
		return fmt.Errorf("provider %q capability %q requires probe image", providerName, capability.Type)
	}
	if len(capability.Probe.Command) == 0 {
		return fmt.Errorf("provider %q capability %q requires probe command", providerName, capability.Type)
	}
	return nil
}

func validateBindingSchema(providerName string, bindingSchema BindingSchema) error {
	if !operationTypePattern.MatchString(bindingSchema.Kind) {
		return fmt.Errorf("provider %q binding kind %q must be fully qualified as provider.kind", providerName, bindingSchema.Kind)
	}
	prefix := providerName + "."
	if len(bindingSchema.Kind) <= len(prefix) || bindingSchema.Kind[:len(prefix)] != prefix {
		return fmt.Errorf("provider %q binding kind %q must use provider prefix %q", providerName, bindingSchema.Kind, prefix)
	}
	return nil
}
