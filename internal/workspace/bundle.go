package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func LoadBundleProviders(baseDir string, refs []BundleRef) ([]Provider, error) {
	var providers []Provider
	seen := map[string]struct{}{}
	for i, ref := range refs {
		if ref.Name == "" {
			return nil, fmt.Errorf("spec.bundleRefs[%d].name is required", i)
		}
		if !idPattern.MatchString(ref.Name) {
			return nil, fmt.Errorf("spec.bundleRefs[%d].name must match %s", i, idPattern.String())
		}
		if _, ok := seen[ref.Name]; ok {
			return nil, fmt.Errorf("spec.bundleRefs[%d].name %q is duplicated", i, ref.Name)
		}
		seen[ref.Name] = struct{}{}
		if ref.Source == "" {
			return nil, fmt.Errorf("spec.bundleRefs[%d].source is required", i)
		}
		if strings.HasPrefix(ref.Source, "builtin:") {
			continue
		}
		if strings.HasPrefix(ref.Source, "git::") || strings.HasPrefix(ref.Source, "oci://") {
			return nil, fmt.Errorf("spec.bundleRefs[%d].source %q is not supported before bundle locking", i, ref.Source)
		}
		provider, err := loadLocalBundleProvider(baseDir, ref)
		if err != nil {
			return nil, fmt.Errorf("spec.bundleRefs[%d]: %w", i, err)
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

func loadLocalBundleProvider(baseDir string, ref BundleRef) (Provider, error) {
	path := ref.Source
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return Provider{}, err
	}
	if info.IsDir() {
		path = filepath.Join(path, "bundle.yaml")
		if _, err := os.Stat(path); err != nil {
			return Provider{}, err
		}
	}
	bundle, err := loadYAML[IntegrationBundle](path)
	if err != nil {
		return Provider{}, err
	}
	if err := validateIntegrationBundle(ref, bundle); err != nil {
		return Provider{}, err
	}
	return Provider{
		Name:           bundle.Metadata.Name,
		Capabilities:   bundle.Spec.Capabilities,
		BindingSchemas: bundle.Spec.BindingSchemas,
	}, nil
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
	return nil
}
