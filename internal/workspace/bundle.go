package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func LoadBundleProviders(baseDir string, refs []BundleRef) ([]Provider, error) {
	providers, _, err := LoadBundleProvidersAndCatalogPaths(baseDir, refs)
	return providers, err
}

func LoadBundleProvidersAndCatalogPaths(baseDir string, refs []BundleRef) ([]Provider, []string, error) {
	var providers []Provider
	var catalogPaths []string
	seen := map[string]struct{}{}
	for i, ref := range refs {
		if ref.Name == "" {
			return nil, nil, fmt.Errorf("spec.bundleRefs[%d].name is required", i)
		}
		if !idPattern.MatchString(ref.Name) {
			return nil, nil, fmt.Errorf("spec.bundleRefs[%d].name must match %s", i, idPattern.String())
		}
		if _, ok := seen[ref.Name]; ok {
			return nil, nil, fmt.Errorf("spec.bundleRefs[%d].name %q is duplicated", i, ref.Name)
		}
		seen[ref.Name] = struct{}{}
		if ref.Source == "" {
			return nil, nil, fmt.Errorf("spec.bundleRefs[%d].source is required", i)
		}
		if strings.HasPrefix(ref.Source, "builtin:") {
			name := strings.TrimPrefix(ref.Source, "builtin:")
			if name == "" {
				return nil, nil, fmt.Errorf("spec.bundleRefs[%d].source must name a built-in provider after builtin:", i)
			}
			if name != ref.Name {
				return nil, nil, fmt.Errorf("spec.bundleRefs[%d].source %q does not match bundleRef name %q", i, ref.Source, ref.Name)
			}
			if _, ok := BuiltInProvider(name); !ok {
				return nil, nil, fmt.Errorf("spec.bundleRefs[%d].source references unknown built-in provider %q", i, name)
			}
			continue
		}
		if strings.HasPrefix(ref.Source, "git::") || strings.HasPrefix(ref.Source, "oci://") {
			return nil, nil, fmt.Errorf("spec.bundleRefs[%d].source %q is not supported before bundle locking", i, ref.Source)
		}
		provider, paths, err := loadLocalBundleProvider(baseDir, ref)
		if err != nil {
			return nil, nil, fmt.Errorf("spec.bundleRefs[%d]: %w", i, err)
		}
		providers = append(providers, provider)
		catalogPaths = append(catalogPaths, paths...)
	}
	return providers, catalogPaths, nil
}

func loadLocalBundleProvider(baseDir string, ref BundleRef) (Provider, []string, error) {
	path := ref.Source
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return Provider{}, nil, err
	}
	if info.IsDir() {
		path = filepath.Join(path, "bundle.yaml")
		if _, err := os.Stat(path); err != nil {
			return Provider{}, nil, err
		}
	}
	bundle, err := loadYAML[IntegrationBundle](path)
	if err != nil {
		return Provider{}, nil, err
	}
	if err := validateIntegrationBundle(ref, bundle); err != nil {
		return Provider{}, nil, err
	}
	if err := resolveBundleSchemaRefs(filepath.Dir(path), &bundle); err != nil {
		return Provider{}, nil, err
	}
	catalogPaths, err := resolveBundleCatalogPaths(filepath.Dir(path), bundle)
	if err != nil {
		return Provider{}, nil, err
	}
	return Provider{
		Name:           bundle.Metadata.Name,
		Capabilities:   bundle.Spec.Capabilities,
		BindingSchemas: bundle.Spec.BindingSchemas,
	}, catalogPaths, nil
}

func validateIntegrationBundle(ref BundleRef, bundle IntegrationBundle) error {
	if bundle.APIVersion != "spex.bundle.v0.1" {
		return fmt.Errorf("unsupported apiVersion %q", bundle.APIVersion)
	}
	if bundle.Kind != "IntegrationBundle" {
		return fmt.Errorf("kind must be IntegrationBundle")
	}
	if bundle.Metadata.Name != ref.Name {
		return fmt.Errorf("metadata.name %q does not match bundleRef name %q", bundle.Metadata.Name, ref.Name)
	}
	if ref.Version != "" && bundle.Metadata.Version != "" && bundle.Metadata.Version != ref.Version {
		return fmt.Errorf("metadata.version %q does not match bundleRef version %q", bundle.Metadata.Version, ref.Version)
	}
	if len(bundle.Spec.Capabilities) == 0 {
		return fmt.Errorf("spec.capabilities must contain at least one capability")
	}
	for i, capability := range bundle.Spec.Capabilities {
		if capability.Type == "" {
			return fmt.Errorf("spec.capabilities[%d].type is required", i)
		}
		if capability.BindingKind == "" {
			return fmt.Errorf("spec.capabilities[%d].bindingKind is required", i)
		}
		if len(capability.Probe.Command) == 0 {
			return fmt.Errorf("spec.capabilities[%d].probe.command is required", i)
		}
		if capability.Probe.Input.Path == "" {
			return fmt.Errorf("spec.capabilities[%d].probe.input.path is required", i)
		}
		if capability.Probe.Output.Path == "" {
			return fmt.Errorf("spec.capabilities[%d].probe.output.path is required", i)
		}
	}
	for i, ref := range bundle.Spec.StepCatalogs {
		if strings.TrimSpace(ref) == "" {
			return fmt.Errorf("spec.stepCatalogs[%d] is required", i)
		}
	}
	for i, ref := range bundle.Spec.FlowCatalogs {
		if strings.TrimSpace(ref) == "" {
			return fmt.Errorf("spec.flowCatalogs[%d] is required", i)
		}
	}
	return nil
}

func resolveBundleCatalogPaths(baseDir string, bundle IntegrationBundle) ([]string, error) {
	var paths []string
	for i, ref := range bundle.Spec.StepCatalogs {
		path := resolveSuiteFile(baseDir, ref)
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("spec.stepCatalogs[%d]: %w", i, err)
		}
		paths = append(paths, path)
	}
	for i, ref := range bundle.Spec.FlowCatalogs {
		path := resolveSuiteFile(baseDir, ref)
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("spec.flowCatalogs[%d]: %w", i, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func resolveBundleSchemaRefs(baseDir string, bundle *IntegrationBundle) error {
	for i := range bundle.Spec.Capabilities {
		capability := &bundle.Spec.Capabilities[i]
		if err := resolveBundleSchemaRef(baseDir, &capability.InputSchema); err != nil {
			return fmt.Errorf("spec.capabilities[%d].inputSchema: %w", i, err)
		}
		if err := resolveBundleSchemaRef(baseDir, &capability.ResultSchema); err != nil {
			return fmt.Errorf("spec.capabilities[%d].resultSchema: %w", i, err)
		}
	}
	for i := range bundle.Spec.BindingSchemas {
		if err := resolveBundleSchemaRef(baseDir, &bundle.Spec.BindingSchemas[i].Schema); err != nil {
			return fmt.Errorf("spec.bindingSchemas[%d].schema: %w", i, err)
		}
	}
	return nil
}

func resolveBundleSchemaRef(baseDir string, ref *SchemaRef) error {
	if ref == nil || ref.Path == "" {
		return nil
	}
	path := resolveSuiteFile(baseDir, ref.Path)
	schema, err := loadYAML[JSONSchema](path)
	if err != nil {
		return err
	}
	ref.Schema = &schema
	return nil
}
