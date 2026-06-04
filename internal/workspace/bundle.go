package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/retry"
)

var ociBundleDigestPattern = regexp.MustCompile(`^oci://.+@sha256:[a-f0-9]{64}$`)

type ociBundleRef struct {
	Repository string
	Digest     string
}

var fetchOCIBundleRef = fetchOCIBundleRefWithORAS

func LoadBundleProviders(baseDir string, refs []BundleRef) ([]Provider, error) {
	providers, _, err := LoadBundleProvidersAndCatalogPaths(baseDir, refs)
	return providers, err
}

func LoadBundleProvidersAndCatalogPaths(baseDir string, refs []BundleRef) ([]Provider, []string, error) {
	bundles, err := LoadResolvedBundles(baseDir, refs)
	if err != nil {
		return nil, nil, err
	}
	var providers []Provider
	var catalogPaths []string
	for _, bundle := range bundles {
		providers = append(providers, bundle.Provider)
		catalogPaths = append(catalogPaths, bundle.CatalogPaths...)
	}
	return providers, catalogPaths, nil
}

func LoadResolvedBundles(baseDir string, refs []BundleRef) ([]ResolvedBundle, error) {
	var bundles []ResolvedBundle
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
			name := strings.TrimPrefix(ref.Source, "builtin:")
			if name == "" {
				return nil, fmt.Errorf("spec.bundleRefs[%d].source must name a built-in provider after builtin:", i)
			}
			if name != ref.Name {
				return nil, fmt.Errorf("spec.bundleRefs[%d].source %q does not match bundleRef name %q", i, ref.Source, ref.Name)
			}
			provider, ok := BuiltInProvider(name)
			if !ok {
				return nil, fmt.Errorf("spec.bundleRefs[%d].source references unknown built-in provider %q", i, name)
			}
			bundles = append(bundles, ResolvedBundle{
				Name:       ref.Name,
				Version:    ref.Version,
				Source:     ref.Source,
				SourceType: "builtin",
				Provider:   provider,
			})
			continue
		}
		if strings.HasPrefix(ref.Source, "oci://") {
			ociRef, err := parseOCIBundleSource(ref.Source)
			if err != nil {
				return nil, fmt.Errorf("spec.bundleRefs[%d].source: %w", i, err)
			}
			path, err := checkoutOCIBundleRef(baseDir, ref.Source, ociRef)
			if err != nil {
				return nil, fmt.Errorf("spec.bundleRefs[%d].source: %w", i, err)
			}
			bundle, err := loadLocalBundle(baseDir, BundleRef{Name: ref.Name, Version: ref.Version, Source: path})
			if err != nil {
				return nil, fmt.Errorf("spec.bundleRefs[%d]: %w", i, err)
			}
			bundle.Source = ref.Source
			bundle.SourceType = "oci"
			bundle.ResolvedRevision = ociRef.Digest
			bundles = append(bundles, bundle)
			continue
		}
		if gitRef, ok, err := parseGitFileRef(ref.Source); err != nil {
			return nil, fmt.Errorf("spec.bundleRefs[%d].source: %w", i, err)
		} else if ok {
			path, err := checkoutGitFileRef(baseDir, gitRef)
			if err != nil {
				return nil, fmt.Errorf("spec.bundleRefs[%d].source: %w", i, err)
			}
			bundle, err := loadLocalBundle(baseDir, BundleRef{Name: ref.Name, Version: ref.Version, Source: path})
			if err != nil {
				return nil, fmt.Errorf("spec.bundleRefs[%d]: %w", i, err)
			}
			bundle.Source = ref.Source
			bundle.SourceType = "git"
			revision, err := runGitOutput(path, "rev-parse", "HEAD")
			if err != nil {
				return nil, fmt.Errorf("spec.bundleRefs[%d].source: %w", i, err)
			}
			bundle.ResolvedRevision = revision
			bundles = append(bundles, bundle)
			continue
		}
		bundle, err := loadLocalBundle(baseDir, ref)
		if err != nil {
			return nil, fmt.Errorf("spec.bundleRefs[%d]: %w", i, err)
		}
		bundles = append(bundles, bundle)
	}
	return bundles, nil
}

func parseOCIBundleSource(source string) (ociBundleRef, error) {
	if !strings.Contains(source, "@sha256:") {
		return ociBundleRef{}, fmt.Errorf("OCI bundle refs must be pinned by digest using @sha256:<64 lowercase hex chars>")
	}
	if !ociBundleDigestPattern.MatchString(source) {
		return ociBundleRef{}, fmt.Errorf("OCI bundle ref digest must use sha256 with 64 lowercase hex chars")
	}
	withoutScheme := strings.TrimPrefix(source, "oci://")
	at := strings.LastIndex(withoutScheme, "@")
	if at <= 0 || at == len(withoutScheme)-1 {
		return ociBundleRef{}, fmt.Errorf("OCI bundle refs must use oci://repository@sha256:<digest>")
	}
	return ociBundleRef{
		Repository: withoutScheme[:at],
		Digest:     withoutScheme[at+1:],
	}, nil
}

func checkoutOCIBundleRef(baseDir, source string, ref ociBundleRef) (string, error) {
	cacheRoot := os.Getenv("SPEX_OCI_BUNDLE_CACHE_DIR")
	if cacheRoot == "" {
		cacheRoot = filepath.Join(baseDir, ".spex", "oci-bundles")
	}
	sum := sha256.Sum256([]byte(source))
	cacheDir := filepath.Join(cacheRoot, hex.EncodeToString(sum[:])[:16])
	if _, err := os.Stat(filepath.Join(cacheDir, "bundle.yaml")); err == nil {
		return cacheDir, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.RemoveAll(cacheDir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	if err := fetchOCIBundleRef(context.Background(), ref, cacheDir); err != nil {
		_ = os.RemoveAll(cacheDir)
		return "", err
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "bundle.yaml")); err != nil {
		_ = os.RemoveAll(cacheDir)
		return "", fmt.Errorf("OCI bundle %s did not contain bundle.yaml at artifact root", ref.Repository)
	}
	return cacheDir, nil
}

func fetchOCIBundleRefWithORAS(ctx context.Context, ref ociBundleRef, targetDir string) error {
	repo, err := remote.NewRepository(ref.Repository)
	if err != nil {
		return err
	}
	repo.Client = retry.DefaultClient
	store, err := file.New(targetDir)
	if err != nil {
		return err
	}
	defer store.Close()
	_, err = oras.Copy(ctx, repo, ref.Digest, store, ref.Digest, oras.DefaultCopyOptions)
	return err
}

func loadLocalBundleProvider(baseDir string, ref BundleRef) (Provider, []string, error) {
	bundle, err := loadLocalBundle(baseDir, ref)
	if err != nil {
		return Provider{}, nil, err
	}
	return bundle.Provider, bundle.CatalogPaths, nil
}

func loadLocalBundle(baseDir string, ref BundleRef) (ResolvedBundle, error) {
	path := ref.Source
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return ResolvedBundle{}, err
	}
	if info.IsDir() {
		path = filepath.Join(path, "bundle.yaml")
		if _, err := os.Stat(path); err != nil {
			return ResolvedBundle{}, err
		}
	}
	bundle, err := loadYAML[IntegrationBundle](path)
	if err != nil {
		return ResolvedBundle{}, err
	}
	if err := validateIntegrationBundle(ref, bundle); err != nil {
		return ResolvedBundle{}, err
	}
	if err := resolveBundleSchemaRefs(filepath.Dir(path), &bundle); err != nil {
		return ResolvedBundle{}, err
	}
	catalogPaths, err := resolveBundleCatalogPaths(filepath.Dir(path), bundle)
	if err != nil {
		return ResolvedBundle{}, err
	}
	provider := Provider{
		Name:           bundle.Metadata.Name,
		Capabilities:   bundle.Spec.Capabilities,
		BindingSchemas: bundle.Spec.BindingSchemas,
	}
	return ResolvedBundle{
		Name:         bundle.Metadata.Name,
		Version:      bundle.Metadata.Version,
		Source:       ref.Source,
		SourceType:   "local",
		ManifestPath: path,
		CatalogPaths: catalogPaths,
		Provider:     provider,
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
