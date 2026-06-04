package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var tagPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var parameterNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var graphQLNamePattern = regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]*$`)
var templateRefPattern = regexp.MustCompile(`\$\{([^}]+)\}`)
var matcherPathPattern = regexp.MustCompile(`^\$(\.[A-Za-z_][A-Za-z0-9_]*(\[[0-9]+\])*)*$`)
var configMapDataKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var kafkaTopicNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var fakeServicePattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])(fake|wiremock|mockserver)([^a-z0-9]|$)`)
var secretLiteralPattern = regexp.MustCompile(`(?i)--from-literal=(password|token|client-secret|clientsecret|api[-_]?key|secret)=['"]?[^'$"\s]`)
var commandURLUserinfoPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^/\s:@]+:[^/\s@]+@`)

const maxGitCommandOutputSize int64 = 1 << 20
const maxYAMLInputFileSize int64 = 4 << 20
const maxFeatureInputFileSize int64 = 1 << 20
const maxGraphQLQueryFileSize int64 = 1 << 20

var catalogVariablePattern = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)(?::([A-Za-z]+))?\}`)
var ssmReferencePattern = regexp.MustCompile(`^\s*\{\{\s*ssm\s+"([^"]+)"\s*\}\}\s*$`)

var integrationPlaceholders = map[string]struct{}{
	"repoRoot":              {},
	"integrationProfileDir": {},
	"workspaceDir":          {},
	"kubeconfig":            {},
	"namespace":             {},
	"kubeContext":           {},
	"kubeContextArgs":       {},
	"probeImage":            {},
	"probeImagePullPolicy":  {},
	"kindCluster":           {},
}

func LoadInputs(scenarioPath, bindingPath string) (Inputs, error) {
	return LoadInputsWithCatalogs(scenarioPath, bindingPath, CatalogBundle{})
}

func LoadInputsWithCatalogs(scenarioPath, bindingPath string, catalogs CatalogBundle) (Inputs, error) {
	inputs, err := LoadInputsWithCatalogsMany(scenarioPath, bindingPath, catalogs)
	if err != nil {
		return Inputs{}, err
	}
	if len(inputs) != 1 {
		return Inputs{}, fmt.Errorf("scenario: %s contains %d scenarios; use a suite to compile multi-scenario feature files", scenarioPath, len(inputs))
	}
	return inputs[0], nil
}

func LoadInputsWithCatalogsMany(scenarioPath, bindingPath string, catalogs CatalogBundle) ([]Inputs, error) {
	return LoadInputsWithCatalogsManyAndProviders(scenarioPath, bindingPath, catalogs, nil)
}

func LoadInputsWithCatalogsManyAndProviders(scenarioPath, bindingPath string, catalogs CatalogBundle, providers []Provider) ([]Inputs, error) {
	scenarios, err := loadScenarioFile(scenarioPath)
	if err != nil {
		return nil, fmt.Errorf("scenario: %w", err)
	}
	binding, err := loadYAML[TargetBinding](bindingPath)
	if err != nil {
		return nil, fmt.Errorf("binding: %w", err)
	}
	if err := validateBinding(binding); err != nil {
		return nil, fmt.Errorf("binding: %w", err)
	}
	var out []Inputs
	for _, scenario := range scenarios {
		if err := expandScenarioFromCatalogs(&scenario, catalogs); err != nil {
			return nil, fmt.Errorf("scenario %q: %w", scenario.Metadata.Name, err)
		}
		if err := validateScenarioWithProviders(scenario, scenarioPath, providers); err != nil {
			return nil, fmt.Errorf("scenario %q: %w", scenario.Metadata.Name, err)
		}
		if err := validateScenarioBindingWithProviders(scenario, binding, providers); err != nil {
			return nil, err
		}
		out = append(out, Inputs{
			ScenarioPath: scenarioPath,
			BindingPath:  bindingPath,
			ScenarioName: scenario.Metadata.Name,
			Namespace:    binding.Spec.Namespace,
			KubeContext:  binding.Spec.KubeContext,
			RunID:        "run-" + time.Now().UTC().Format("20060102T150405Z"),
			Providers:    providers,
			Scenario:     scenario,
			Binding:      binding,
		})
	}
	return out, nil
}

func (r *ScenarioRef) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Value == "" {
			return fmt.Errorf("scenario path is required")
		}
		r.Path = value.Value
		return nil
	case yaml.MappingNode:
		allowed := map[string]struct{}{
			"path":                  {},
			"bindingRef":            {},
			"integrationProfileRef": {},
			"parameters":            {},
			"tags":                  {},
		}
		for i := 0; i < len(value.Content); i += 2 {
			key := value.Content[i].Value
			if _, ok := allowed[key]; !ok {
				return fmt.Errorf("unknown field %q", key)
			}
		}
		type rawScenarioRef ScenarioRef
		var raw rawScenarioRef
		if err := value.Decode(&raw); err != nil {
			return err
		}
		*r = ScenarioRef(raw)
		return nil
	default:
		return fmt.Errorf("scenario entry must be a path string or mapping")
	}
}

func loadScenarioFile(path string) ([]Scenario, error) {
	if strings.HasSuffix(path, ".feature") {
		return loadFeatureScenarios(path)
	}
	scenario, err := loadYAML[Scenario](path)
	if err != nil {
		return nil, err
	}
	return []Scenario{scenario}, nil
}

func loadFeatureScenarios(path string) ([]Scenario, error) {
	content, err := readRegularInputFile(path, maxFeatureInputFileSize)
	if err != nil {
		return nil, err
	}
	featureText, err := expandScenarioOutlines(string(content))
	if err != nil {
		return nil, err
	}
	featureName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	baseScenario := Scenario{
		APIVersion: "spex.scenario.v0.1",
		Kind:       "Scenario",
		Spec: ScenarioSpec{
			Defaults: Defaults{
				Timeout:      "60s",
				PollInterval: "1s",
			},
			Correlation: Correlation{
				ScenarioRunID: "auto",
				Strategy:      "payloadTemplate",
			},
			Parameters: map[string]Parameter{},
		},
	}
	var featureDescription string
	var featureTags []string
	var background []StepInvocation
	var scenarios []Scenario
	var current *Scenario
	var lastKind string
	inBackground := false
	seenSlugs := map[string]struct{}{}
	var pendingTags []string
	for lineNo, rawLine := range strings.Split(featureText, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "@") {
			tags, err := parseGherkinTagLine(line)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo+1, err)
			}
			pendingTags = append(pendingTags, tags...)
			continue
		}
		switch {
		case strings.HasPrefix(line, "Feature:"):
			if len(scenarios) > 0 || current != nil || len(background) > 0 {
				return nil, fmt.Errorf("line %d: Feature must appear before Background or Scenario", lineNo+1)
			}
			featureDescription = strings.TrimSpace(strings.TrimPrefix(line, "Feature:"))
			featureTags = append(featureTags, pendingTags...)
			pendingTags = nil
		case strings.HasPrefix(line, "Background:"):
			if len(scenarios) > 0 || current != nil {
				return nil, fmt.Errorf("line %d: Background must appear before Scenario", lineNo+1)
			}
			if len(pendingTags) > 0 {
				return nil, fmt.Errorf("line %d: tags cannot be attached to Background", lineNo+1)
			}
			inBackground = true
			lastKind = ""
		case strings.HasPrefix(line, "Scenario:"):
			if current != nil {
				scenarios = append(scenarios, *current)
			}
			name := strings.TrimSpace(strings.TrimPrefix(line, "Scenario:"))
			if name == "" {
				return nil, fmt.Errorf("line %d: Scenario name is required", lineNo+1)
			}
			slug := DNSLabel(name)
			if _, exists := seenSlugs[slug]; exists {
				return nil, fmt.Errorf("line %d: duplicate Scenario slug %q", lineNo+1, slug)
			}
			seenSlugs[slug] = struct{}{}
			scenario := baseScenario
			scenario.Spec.Parameters = map[string]Parameter{}
			if featureDescription != "" {
				scenario.Spec.Description = featureDescription
			}
			scenario.Metadata = Metadata{Name: slug, Tags: mergeTags(featureTags, pendingTags)}
			pendingTags = nil
			scenario.Spec.StepInvocations = append([]StepInvocation{}, background...)
			current = &scenario
			inBackground = false
			lastKind = ""
		case strings.HasPrefix(line, "Scenario Outline:") || strings.HasPrefix(line, "Scenario Template:"):
			return nil, fmt.Errorf("line %d: Scenario Outline must contain an Examples table", lineNo+1)
		case strings.HasPrefix(line, "Examples:"):
			return nil, fmt.Errorf("line %d: Examples tables are not supported", lineNo+1)
		case strings.HasPrefix(line, "|"):
			return nil, fmt.Errorf("line %d: Gherkin tables are not supported", lineNo+1)
		case hasGherkinStepPrefix(line):
			if current == nil && !inBackground {
				return nil, fmt.Errorf("line %d: step appears before Background or Scenario", lineNo+1)
			}
			kind, text := splitGherkinStep(line)
			if kind == "and" || kind == "but" {
				if lastKind == "" {
					return nil, fmt.Errorf("line %d: %s cannot be the first step", lineNo+1, kind)
				}
				kind = lastKind
			}
			lastKind = kind
			step := StepInvocation{Kind: kind, Text: text}
			if inBackground {
				background = append(background, step)
			} else {
				current.Spec.StepInvocations = append(current.Spec.StepInvocations, step)
			}
		default:
			return nil, fmt.Errorf("line %d: unsupported Gherkin line %q", lineNo+1, line)
		}
	}
	if current != nil {
		scenarios = append(scenarios, *current)
	}
	if len(pendingTags) > 0 {
		return nil, fmt.Errorf("tags must be followed by Feature or Scenario")
	}
	if len(scenarios) == 0 {
		if len(background) > 0 {
			return nil, fmt.Errorf("feature must contain at least one Scenario")
		}
		scenario := baseScenario
		if featureDescription != "" {
			scenario.Spec.Description = featureDescription
		}
		scenario.Metadata = Metadata{Name: DNSLabel(featureName), Tags: featureTags}
		scenario.Spec.Parameters = map[string]Parameter{}
		scenarios = append(scenarios, scenario)
	}
	for _, scenario := range scenarios {
		if len(scenario.Spec.StepInvocations) == 0 {
			return nil, fmt.Errorf("Scenario %q must contain at least one step", scenario.Metadata.Name)
		}
	}
	return scenarios, nil
}

func parseGherkinTagLine(line string) ([]string, error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil, fmt.Errorf("tag line is empty")
	}
	var tags []string
	for _, field := range fields {
		if !strings.HasPrefix(field, "@") || len(field) == 1 {
			return nil, fmt.Errorf("invalid tag %q", field)
		}
		tag := strings.TrimPrefix(field, "@")
		if err := validateTag(tag); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, nil
}

func expandScenarioOutlines(content string) (string, error) {
	lines := strings.Split(content, "\n")
	var out []string
	var pendingTagLines []string
	flushTags := func() {
		out = append(out, pendingTagLines...)
		pendingTagLines = nil
	}
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "@") {
			pendingTagLines = append(pendingTagLines, lines[i])
			continue
		}
		if !strings.HasPrefix(line, "Scenario Outline:") && !strings.HasPrefix(line, "Scenario Template:") {
			flushTags()
			out = append(out, lines[i])
			continue
		}
		prefix := "Scenario Outline:"
		if strings.HasPrefix(line, "Scenario Template:") {
			prefix = "Scenario Template:"
		}
		name := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if name == "" {
			return "", fmt.Errorf("line %d: Scenario Outline name is required", i+1)
		}
		var steps []string
		i++
		for ; i < len(lines); i++ {
			current := strings.TrimSpace(lines[i])
			if current == "" || strings.HasPrefix(current, "#") {
				steps = append(steps, lines[i])
				continue
			}
			if strings.HasPrefix(current, "Examples:") {
				break
			}
			if strings.HasPrefix(current, "Scenario:") || strings.HasPrefix(current, "Scenario Outline:") || strings.HasPrefix(current, "Scenario Template:") || strings.HasPrefix(current, "Background:") || strings.HasPrefix(current, "Feature:") {
				return "", fmt.Errorf("line %d: Scenario Outline must contain an Examples table", i+1)
			}
			steps = append(steps, lines[i])
		}
		if i >= len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[i]), "Examples:") {
			return "", fmt.Errorf("line %d: Scenario Outline must contain an Examples table", i+1)
		}
		var table []string
		i++
		for ; i < len(lines); i++ {
			current := strings.TrimSpace(lines[i])
			if current == "" || strings.HasPrefix(current, "#") {
				continue
			}
			if !strings.HasPrefix(current, "|") {
				i--
				break
			}
			table = append(table, current)
		}
		rows, err := parseExamplesTable(table)
		if err != nil {
			return "", err
		}
		for rowIndex, row := range rows {
			out = append(out, pendingTagLines...)
			out = append(out, fmt.Sprintf("  Scenario: %s %d", renderOutlineValue(name, row), rowIndex+1))
			for _, step := range steps {
				out = append(out, renderOutlineValue(step, row))
			}
		}
		pendingTagLines = nil
	}
	flushTags()
	return strings.Join(out, "\n"), nil
}

func parseExamplesTable(table []string) ([]map[string]string, error) {
	if len(table) < 2 {
		return nil, fmt.Errorf("Examples table must contain a header and at least one row")
	}
	headers := splitExamplesRow(table[0])
	if len(headers) == 0 {
		return nil, fmt.Errorf("Examples table header must not be empty")
	}
	seen := map[string]struct{}{}
	for _, header := range headers {
		if !parameterNamePattern.MatchString(header) {
			return nil, fmt.Errorf("Examples header %q must match %s", header, parameterNamePattern.String())
		}
		if _, ok := seen[header]; ok {
			return nil, fmt.Errorf("Examples table contains duplicate header %q", header)
		}
		seen[header] = struct{}{}
	}
	var rows []map[string]string
	for i, line := range table[1:] {
		values := splitExamplesRow(line)
		if len(values) != len(headers) {
			return nil, fmt.Errorf("Examples row %d has %d value(s), want %d", i+1, len(values), len(headers))
		}
		row := map[string]string{}
		for j, header := range headers {
			row[header] = values[j]
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func splitExamplesRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

func renderOutlineValue(value string, row map[string]string) string {
	out := value
	for k, v := range row {
		out = strings.ReplaceAll(out, "<"+k+">", v)
	}
	return out
}

func validateTag(tag string) error {
	if !tagPattern.MatchString(tag) {
		return fmt.Errorf("tag %q must match %s", tag, tagPattern.String())
	}
	return nil
}

func mergeTags(groups ...[]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, group := range groups {
		for _, tag := range group {
			if _, exists := seen[tag]; exists {
				continue
			}
			seen[tag] = struct{}{}
			out = append(out, tag)
		}
	}
	return out
}

func hasGherkinStepPrefix(line string) bool {
	for _, prefix := range []string{"Given ", "When ", "Then ", "And ", "But "} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func splitGherkinStep(line string) (string, string) {
	parts := strings.SplitN(line, " ", 2)
	return strings.ToLower(parts[0]), strings.TrimSpace(parts[1])
}

func LoadCatalogBundle(paths []string) (CatalogBundle, error) {
	bundle := CatalogBundle{Flows: map[string]FlowDefinition{}}
	for _, catalogPath := range paths {
		var header struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
		}
		if err := loadYAMLHeader(catalogPath, &header); err != nil {
			return CatalogBundle{}, err
		}
		switch header.Kind {
		case "FlowCatalog":
			catalog, err := loadYAML[FlowCatalog](catalogPath)
			if err != nil {
				return CatalogBundle{}, err
			}
			if err := validateFlowCatalog(catalog); err != nil {
				return CatalogBundle{}, fmt.Errorf("%s: %w", catalogPath, err)
			}
			for name, flow := range catalog.Spec.Flows {
				if _, exists := bundle.Flows[name]; exists {
					return CatalogBundle{}, fmt.Errorf("%s: duplicate flow %q", catalogPath, name)
				}
				bundle.Flows[name] = flow
				bundle.Inventory.Flows = append(bundle.Inventory.Flows, CatalogFlow{Name: name, Source: catalogPath, Flow: flow})
			}
		case "StepCatalog":
			catalog, err := loadYAML[StepCatalog](catalogPath)
			if err != nil {
				return CatalogBundle{}, err
			}
			if err := validateStepCatalog(catalog); err != nil {
				return CatalogBundle{}, fmt.Errorf("%s: %w", catalogPath, err)
			}
			for _, step := range catalog.Spec.Steps {
				bundle.Steps = append(bundle.Steps, step)
				bundle.Inventory.Steps = append(bundle.Inventory.Steps, CatalogStep{Source: catalogPath, Step: step})
			}
		default:
			return CatalogBundle{}, fmt.Errorf("%s: unsupported catalog kind %q", catalogPath, header.Kind)
		}
	}
	return bundle, nil
}

func loadYAMLHeader(path string, out any) error {
	content, err := readRegularInputFile(path, maxYAMLInputFileSize)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(content, out)
}

func LoadIntegrationProfile(path string) (IntegrationProfile, error) {
	return loadIntegrationProfile(path, map[string]bool{})
}

func loadIntegrationProfile(path string, seen map[string]bool) (IntegrationProfile, error) {
	cleanPath := filepath.Clean(path)
	if seen[cleanPath] {
		return IntegrationProfile{}, fmt.Errorf("integration profile extends cycle at %s", cleanPath)
	}
	seen[cleanPath] = true
	profile, err := loadYAML[IntegrationProfile](path)
	if err != nil {
		return IntegrationProfile{}, err
	}
	for _, parentRef := range profile.Spec.Extends {
		parentPath := resolveScenarioFile(path, parentRef)
		parent, err := loadIntegrationProfile(parentPath, seen)
		if err != nil {
			return IntegrationProfile{}, fmt.Errorf("spec.extends %q: %w", parentRef, err)
		}
		profile = mergeIntegrationProfiles(parent, profile)
	}
	if profile.Spec.KIND.Config != "" {
		profile.Spec.KIND.Config = resolveScenarioFile(path, profile.Spec.KIND.Config)
	}
	if err := validateIntegrationProfile(profile); err != nil {
		return IntegrationProfile{}, err
	}
	return profile, nil
}

func mergeIntegrationProfiles(parent, child IntegrationProfile) IntegrationProfile {
	out := parent
	out.APIVersion = child.APIVersion
	out.Kind = child.Kind
	out.Spec.AllowFakes = parent.Spec.AllowFakes || child.Spec.AllowFakes
	if child.Spec.KIND.Start {
		out.Spec.KIND.Start = true
	}
	if child.Spec.KIND.ClusterName != "" {
		out.Spec.KIND.ClusterName = child.Spec.KIND.ClusterName
	}
	if child.Spec.KIND.Config != "" {
		out.Spec.KIND.Config = child.Spec.KIND.Config
	}
	if child.Spec.KIND.NodeCache != nil {
		out.Spec.KIND.NodeCache = child.Spec.KIND.NodeCache
	}
	out.Spec.KIND.Containers = append(out.Spec.KIND.Containers, child.Spec.KIND.Containers...)
	out.Spec.KIND.Commands = append(out.Spec.KIND.Commands, child.Spec.KIND.Commands...)
	out.Spec.Setup.Commands = append(out.Spec.Setup.Commands, child.Spec.Setup.Commands...)
	out.Spec.HelmApps = append(out.Spec.HelmApps, child.Spec.HelmApps...)
	out.Spec.Extends = nil
	return out
}

func LoadScenarioSuite(suitePath string) (ResolvedScenarioSuite, error) {
	absSuitePath, err := filepath.Abs(suitePath)
	if err != nil {
		return ResolvedScenarioSuite{}, err
	}
	suitePath = absSuitePath
	suite, err := loadYAML[ScenarioSuite](suitePath)
	if err != nil {
		return ResolvedScenarioSuite{}, err
	}
	if err := validateScenarioSuite(suite); err != nil {
		return ResolvedScenarioSuite{}, err
	}
	baseDir := filepath.Dir(suitePath)
	bindingPath, err := resolveSuiteRef(baseDir, suite.Spec.BindingRef)
	if err != nil {
		return ResolvedScenarioSuite{}, fmt.Errorf("spec.bindingRef: %w", err)
	}
	if _, err := os.Stat(bindingPath); err != nil {
		return ResolvedScenarioSuite{}, fmt.Errorf("spec.bindingRef: %w", err)
	}
	integrationProfilePath := ""
	if suite.Spec.IntegrationProfileRef != "" {
		integrationProfilePath, err = resolveSuiteRef(baseDir, suite.Spec.IntegrationProfileRef)
		if err != nil {
			return ResolvedScenarioSuite{}, fmt.Errorf("spec.integrationProfileRef: %w", err)
		}
		if _, err := os.Stat(integrationProfilePath); err != nil {
			return ResolvedScenarioSuite{}, fmt.Errorf("spec.integrationProfileRef: %w", err)
		}
	}
	var catalogPaths []string
	bundles, err := LoadResolvedBundles(baseDir, suite.Spec.BundleRefs)
	if err != nil {
		return ResolvedScenarioSuite{}, err
	}
	var providers []Provider
	for _, bundle := range bundles {
		providers = append(providers, bundle.Provider)
		catalogPaths = append(catalogPaths, bundle.CatalogPaths...)
	}
	for i, ref := range suite.Spec.CatalogRefs {
		catalogPath, err := resolveSuiteRef(baseDir, ref)
		if err != nil {
			return ResolvedScenarioSuite{}, fmt.Errorf("spec.catalogRefs[%d]: %w", i, err)
		}
		if _, err := os.Stat(catalogPath); err != nil {
			return ResolvedScenarioSuite{}, fmt.Errorf("spec.catalogRefs[%d]: %w", i, err)
		}
		catalogPaths = append(catalogPaths, catalogPath)
	}
	scenarioRefs, err := expandSuiteScenarioRefs(baseDir, suite.Spec.Scenarios, bindingPath, integrationProfilePath)
	if err != nil {
		return ResolvedScenarioSuite{}, err
	}
	if len(scenarioRefs) == 0 {
		return ResolvedScenarioSuite{}, fmt.Errorf("spec.scenarios did not match any scenario files")
	}
	var scenarioPaths []string
	for _, ref := range scenarioRefs {
		scenarioPaths = append(scenarioPaths, ref.Path)
	}
	return ResolvedScenarioSuite{
		Path:                   suitePath,
		Suite:                  suite,
		BindingPath:            bindingPath,
		IntegrationProfilePath: integrationProfilePath,
		CatalogPaths:           catalogPaths,
		Bundles:                bundles,
		Providers:              providers,
		ScenarioPaths:          scenarioPaths,
		ScenarioRefs:           scenarioRefs,
	}, nil
}

func validateScenarioSuite(s ScenarioSuite) error {
	if s.APIVersion != "spex.suite.v0.1" {
		return fmt.Errorf("unsupported apiVersion %q", s.APIVersion)
	}
	if s.Kind != "ScenarioSuite" {
		return fmt.Errorf("kind must be ScenarioSuite")
	}
	if !idPattern.MatchString(s.Metadata.Name) {
		return fmt.Errorf("metadata.name must match %s", idPattern.String())
	}
	if strings.TrimSpace(s.Spec.BindingRef) == "" {
		return fmt.Errorf("spec.bindingRef is required")
	}
	if len(s.Spec.Scenarios) == 0 {
		return fmt.Errorf("spec.scenarios must contain at least one pattern")
	}
	for i, ref := range s.Spec.Scenarios {
		if strings.TrimSpace(ref.Path) == "" {
			return fmt.Errorf("spec.scenarios[%d].path is required", i)
		}
		for name := range ref.Parameters {
			if !parameterNamePattern.MatchString(name) {
				return fmt.Errorf("spec.scenarios[%d].parameters contains invalid name %q", i, name)
			}
		}
		for j, tag := range ref.Tags {
			if err := validateTag(tag); err != nil {
				return fmt.Errorf("spec.scenarios[%d].tags[%d]: %w", i, j, err)
			}
		}
	}
	for i, ref := range s.Spec.BundleRefs {
		if strings.TrimSpace(ref.Name) == "" {
			return fmt.Errorf("spec.bundleRefs[%d].name is required", i)
		}
		if strings.TrimSpace(ref.Source) == "" {
			return fmt.Errorf("spec.bundleRefs[%d].source is required", i)
		}
	}
	if err := validateSuiteExecution(s.Spec.Execution); err != nil {
		return err
	}
	seenReportFormats := map[string]bool{}
	for _, format := range s.Spec.Reports.Format {
		switch format {
		case "yaml", "json", "junit":
		default:
			return fmt.Errorf("spec.reports.format contains unsupported format %q", format)
		}
		if seenReportFormats[format] {
			return fmt.Errorf("spec.reports.format contains duplicate format %q", format)
		}
		seenReportFormats[format] = true
	}
	return nil
}

func validateSuiteExecution(execution SuiteExecution) error {
	if execution.Repetitions < 0 {
		return fmt.Errorf("spec.execution.repetitions must be greater than or equal to 0")
	}
	if execution.Concurrency < 0 {
		return fmt.Errorf("spec.execution.concurrency must be greater than or equal to 0")
	}
	if execution.RateLimit.PerSecond < 0 {
		return fmt.Errorf("spec.execution.rateLimit.perSecond must be greater than or equal to 0")
	}
	if execution.MaxFailures < 0 {
		return fmt.Errorf("spec.execution.maxFailures must be greater than or equal to 0")
	}
	if execution.Concurrency > 0 && execution.Repetitions <= 1 {
		return fmt.Errorf("spec.execution.concurrency requires spec.execution.repetitions greater than 1")
	}
	if execution.Concurrency > 1 && !execution.Isolation.NamespacePerIteration {
		return fmt.Errorf("spec.execution.concurrency greater than 1 requires spec.execution.isolation.namespacePerIteration")
	}
	if execution.RateLimit.PerSecond > 0 && execution.Repetitions <= 1 {
		return fmt.Errorf("spec.execution.rateLimit.perSecond requires spec.execution.repetitions greater than 1")
	}
	return nil
}

func validateFlowCatalog(c FlowCatalog) error {
	if c.APIVersion != "spex.catalog.v0.1" {
		return fmt.Errorf("unsupported apiVersion %q", c.APIVersion)
	}
	if c.Kind != "FlowCatalog" {
		return fmt.Errorf("kind must be FlowCatalog")
	}
	if !idPattern.MatchString(c.Metadata.Name) {
		return fmt.Errorf("metadata.name must match %s", idPattern.String())
	}
	if len(c.Spec.Flows) == 0 {
		return fmt.Errorf("spec.flows must contain at least one flow")
	}
	for name, flow := range c.Spec.Flows {
		if !idPattern.MatchString(name) {
			return fmt.Errorf("spec.flows contains invalid name %q", name)
		}
		if len(flow.ExpandsTo.Operations) == 0 {
			return fmt.Errorf("spec.flows.%s.expandsTo.operations must contain at least one operation", name)
		}
	}
	return nil
}

func validateStepCatalog(c StepCatalog) error {
	if c.APIVersion != "spex.catalog.v0.1" {
		return fmt.Errorf("unsupported apiVersion %q", c.APIVersion)
	}
	if c.Kind != "StepCatalog" {
		return fmt.Errorf("kind must be StepCatalog")
	}
	if !idPattern.MatchString(c.Metadata.Name) {
		return fmt.Errorf("metadata.name must match %s", idPattern.String())
	}
	if len(c.Spec.Steps) == 0 {
		return fmt.Errorf("spec.steps must contain at least one step")
	}
	for i, step := range c.Spec.Steps {
		if step.Kind != "given" && step.Kind != "when" && step.Kind != "then" && step.Kind != "and" {
			return fmt.Errorf("spec.steps[%d].kind must be given, when, then, or and", i)
		}
		if strings.TrimSpace(step.Expression) == "" {
			return fmt.Errorf("spec.steps[%d].expression is required", i)
		}
		if catalogExpansionIsEmpty(step.Output) {
			return fmt.Errorf("spec.steps[%d].output must contain parameters, payloadTemplates, graphqlQueries, or operations", i)
		}
	}
	return nil
}

func catalogExpansionIsEmpty(expansion CatalogExpansion) bool {
	return len(expansion.Parameters) == 0 &&
		len(expansion.PayloadTemplates) == 0 &&
		len(expansion.GraphQLQueries) == 0 &&
		len(expansion.Operations) == 0
}

func expandScenarioFromCatalogs(s *Scenario, catalogs CatalogBundle) error {
	for _, use := range s.Spec.Use {
		if strings.TrimSpace(use.Flow) == "" {
			return fmt.Errorf("spec.use contains item without flow")
		}
		if !idPattern.MatchString(use.ID) {
			return fmt.Errorf("spec.use flow %q has invalid id %q", use.Flow, use.ID)
		}
		flow, ok := catalogs.Flows[use.Flow]
		if !ok {
			return fmt.Errorf("spec.use references unknown flow %q", use.Flow)
		}
		values := map[string]string{"id": use.ID}
		for k, v := range use.With {
			values[k] = v
		}
		for name := range flow.Parameters {
			if values[name] == "" {
				return fmt.Errorf("spec.use flow %q missing parameter %q", use.Flow, name)
			}
		}
		mergeExpansion(s, renderExpansion(flow.ExpandsTo, values))
	}
	for _, invocation := range s.Spec.StepInvocations {
		values, output, ok, err := matchStepInvocation(invocation, catalogs.Steps)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("stepInvocation %q %q did not match any catalog step. Available %q expressions: %s", invocation.Kind, invocation.Text, invocation.Kind, availableStepExpressions(invocation.Kind, catalogs.Steps))
		}
		mergeExpansion(s, renderExpansion(output, values))
	}
	return nil
}

func availableStepExpressions(kind string, steps []StepDefinition) string {
	var expressions []string
	for _, step := range steps {
		if step.Kind == kind {
			expressions = append(expressions, fmt.Sprintf("%q", step.Expression))
		}
	}
	if len(expressions) == 0 {
		return "(none)"
	}
	sort.Strings(expressions)
	return strings.Join(expressions, ", ")
}

func matchStepInvocation(invocation StepInvocation, steps []StepDefinition) (map[string]string, CatalogExpansion, bool, error) {
	for _, step := range steps {
		if step.Kind != invocation.Kind {
			continue
		}
		values, ok, err := matchCatalogExpression(step.Expression, invocation.Text)
		if err != nil {
			return nil, CatalogExpansion{}, false, err
		}
		if ok {
			return values, step.Output, true, nil
		}
	}
	return nil, CatalogExpansion{}, false, nil
}

func matchCatalogExpression(expression, text string) (map[string]string, bool, error) {
	var names []string
	pattern := regexp.QuoteMeta(expression)
	for _, match := range catalogVariablePattern.FindAllStringSubmatch(expression, -1) {
		name := match[1]
		typ := match[2]
		names = append(names, name)
		token := regexp.QuoteMeta(match[0])
		replacement := `(.+)`
		if typ == "number" {
			replacement = `([+-]?(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+))`
		}
		pattern = strings.Replace(pattern, token, replacement, 1)
	}
	re, err := regexp.Compile("^" + pattern + "$")
	if err != nil {
		return nil, false, err
	}
	matches := re.FindStringSubmatch(text)
	if matches == nil {
		return nil, false, nil
	}
	values := map[string]string{}
	for i, name := range names {
		values[name] = matches[i+1]
	}
	return values, true, nil
}

func mergeExpansion(s *Scenario, expansion CatalogExpansion) {
	if s.Spec.Parameters == nil {
		s.Spec.Parameters = map[string]Parameter{}
	}
	for k, v := range expansion.Parameters {
		s.Spec.Parameters[k] = v
	}
	if s.Spec.PayloadTemplates == nil {
		s.Spec.PayloadTemplates = map[string]PayloadTemplate{}
	}
	for k, v := range expansion.PayloadTemplates {
		s.Spec.PayloadTemplates[k] = v
	}
	if s.Spec.GraphQLQueries == nil {
		s.Spec.GraphQLQueries = map[string]GraphQLQuery{}
	}
	for k, v := range expansion.GraphQLQueries {
		s.Spec.GraphQLQueries[k] = v
	}
	s.Spec.Operations = append(s.Spec.Operations, expansion.Operations...)
}

func renderExpansion(expansion CatalogExpansion, values map[string]string) CatalogExpansion {
	out := CatalogExpansion{
		Parameters:       map[string]Parameter{},
		PayloadTemplates: map[string]PayloadTemplate{},
		GraphQLQueries:   map[string]GraphQLQuery{},
	}
	for k, v := range expansion.Parameters {
		out.Parameters[renderCatalogTemplate(k, values)] = Parameter{Type: renderCatalogTemplate(v.Type, values), Default: renderCatalogTemplate(v.Default, values)}
	}
	for k, v := range expansion.PayloadTemplates {
		out.PayloadTemplates[renderCatalogTemplate(k, values)] = PayloadTemplate{ContentType: renderCatalogTemplate(v.ContentType, values), Body: renderCatalogTemplate(v.Body, values)}
	}
	for k, v := range expansion.GraphQLQueries {
		out.GraphQLQueries[renderCatalogTemplate(k, values)] = GraphQLQuery{File: renderCatalogTemplate(v.File, values)}
	}
	for _, op := range expansion.Operations {
		out.Operations = append(out.Operations, renderCatalogOperation(op, values))
	}
	return out
}

func renderCatalogOperation(op Operation, values map[string]string) Operation {
	op.ID = renderCatalogTemplate(op.ID, values)
	op.Type = renderCatalogTemplate(op.Type, values)
	op.After = renderCatalogTemplate(op.After, values)
	op.Timeout = renderCatalogTemplate(op.Timeout, values)
	if op.DependsOn != nil {
		for i, dependency := range op.DependsOn {
			op.DependsOn[i] = renderCatalogTemplate(dependency, values)
		}
	}
	if op.With != nil {
		op.With = renderCatalogValue(op.With, values).(map[string]any)
	}
	if op.MQTT != nil {
		mqtt := *op.MQTT
		mqtt.Topic = renderCatalogTemplate(mqtt.Topic, values)
		mqtt.PayloadTemplateRef = renderCatalogTemplate(mqtt.PayloadTemplateRef, values)
		mqtt.CorrelationID = renderCatalogTemplate(mqtt.CorrelationID, values)
		mqtt.Timeout = renderCatalogTemplate(mqtt.Timeout, values)
		mqtt.Match = renderCatalogMatchers(mqtt.Match, values)
		op.MQTT = &mqtt
	}
	if op.Redpanda != nil {
		redpanda := *op.Redpanda
		redpanda.TopicRef = renderCatalogTemplate(redpanda.TopicRef, values)
		redpanda.CorrelationID = renderCatalogTemplate(redpanda.CorrelationID, values)
		redpanda.Timeout = renderCatalogTemplate(redpanda.Timeout, values)
		redpanda.Match = renderCatalogMatchers(redpanda.Match, values)
		op.Redpanda = &redpanda
	}
	if op.GraphQL != nil {
		graphql := *op.GraphQL
		graphql.QueryRef = renderCatalogTemplate(graphql.QueryRef, values)
		graphql.Timeout = renderCatalogTemplate(graphql.Timeout, values)
		graphql.Variables = map[string]string{}
		for k, v := range op.GraphQL.Variables {
			graphql.Variables[k] = renderCatalogTemplate(v, values)
		}
		graphql.Match = renderCatalogMatchers(graphql.Match, values)
		op.GraphQL = &graphql
	}
	return op
}

func renderCatalogValue(value any, values map[string]string) any {
	switch typed := value.(type) {
	case string:
		return renderCatalogTemplate(typed, values)
	case map[string]any:
		out := map[string]any{}
		for key, child := range typed {
			out[renderCatalogTemplate(key, values)] = renderCatalogValue(child, values)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = renderCatalogValue(child, values)
		}
		return out
	default:
		return value
	}
}

func renderCatalogMatchers(matchers []Matcher, values map[string]string) []Matcher {
	out := make([]Matcher, len(matchers))
	for i, matcher := range matchers {
		matcher.Path = renderCatalogTemplate(matcher.Path, values)
		matcher.EqualsString = renderCatalogTemplate(matcher.EqualsString, values)
		matcher.EqualsNumber = renderCatalogTemplate(matcher.EqualsNumber, values)
		out[i] = matcher
	}
	return out
}

func renderCatalogTemplate(value string, values map[string]string) string {
	var b strings.Builder
	last := 0
	for _, loc := range catalogVariablePattern.FindAllStringSubmatchIndex(value, -1) {
		start, end := loc[0], loc[1]
		if start > 0 && value[start-1] == '$' {
			continue
		}
		b.WriteString(value[last:start])
		name := value[loc[2]:loc[3]]
		if replacement, ok := values[name]; ok {
			b.WriteString(replacement)
		} else {
			b.WriteString(value[start:end])
		}
		last = end
	}
	if last == 0 {
		return value
	}
	b.WriteString(value[last:])
	return b.String()
}

func resolveSuiteFile(baseDir, ref string) string {
	if filepath.IsAbs(ref) {
		return filepath.Clean(ref)
	}
	if strings.HasPrefix(ref, "file://") {
		path := strings.TrimPrefix(ref, "file://")
		if filepath.IsAbs(path) {
			return filepath.Clean(path)
		}
		return filepath.Clean(filepath.Join(baseDir, path))
	}
	return filepath.Clean(filepath.Join(baseDir, ref))
}

type gitFileRef struct {
	RepoURL string
	Path    string
	Ref     string
}

func resolveSuiteRef(baseDir, ref string) (string, error) {
	if gitRef, ok, err := parseGitFileRef(ref); err != nil {
		return "", err
	} else if ok {
		return checkoutGitFileRef(baseDir, gitRef)
	}
	return resolveSuiteFile(baseDir, ref), nil
}

func parseGitFileRef(ref string) (gitFileRef, bool, error) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "git::") {
		return parseExplicitGitFileRef(strings.TrimPrefix(ref, "git::"))
	}
	if strings.HasPrefix(ref, "git+") {
		return parseExplicitGitFileRef(strings.TrimPrefix(ref, "git+"))
	}
	if strings.Contains(ref, "@") && !strings.HasPrefix(ref, ".") && !strings.HasPrefix(ref, "/") && !strings.HasPrefix(ref, "file://") {
		return parseShorthandGitFileRef(ref)
	}
	return gitFileRef{}, false, nil
}

func parseExplicitGitFileRef(ref string) (gitFileRef, bool, error) {
	at := strings.LastIndex(ref, "@")
	if at < 0 || at == len(ref)-1 {
		return gitFileRef{}, false, fmt.Errorf("git file ref must include @ref")
	}
	withoutRef := ref[:at]
	version := ref[at+1:]
	schemeEnd := strings.Index(withoutRef, "://")
	if schemeEnd < 0 {
		return gitFileRef{}, false, fmt.Errorf("git file ref must include a URL scheme")
	}
	subdirMarker := strings.Index(withoutRef[schemeEnd+3:], "//")
	if subdirMarker < 0 {
		return gitFileRef{}, false, fmt.Errorf("git file ref must use //path/to/file after the repository URL")
	}
	subdirMarker += schemeEnd + 3
	repoURL := withoutRef[:subdirMarker]
	filePath := strings.TrimPrefix(withoutRef[subdirMarker+2:], "/")
	if filePath == "" {
		return gitFileRef{}, false, fmt.Errorf("git file ref path is required")
	}
	return gitFileRef{RepoURL: repoURL, Path: filepath.Clean(filePath), Ref: version}, true, nil
}

func parseShorthandGitFileRef(ref string) (gitFileRef, bool, error) {
	at := strings.LastIndex(ref, "@")
	if at < 0 || at == len(ref)-1 {
		return gitFileRef{}, false, nil
	}
	target := ref[:at]
	version := ref[at+1:]
	parts := strings.Split(target, "/")
	if len(parts) < 3 {
		return gitFileRef{}, false, fmt.Errorf("GitHub-style ref must be owner/repo/path@ref")
	}
	baseURL := strings.TrimRight(os.Getenv("SPEX_GIT_REF_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = "https://github.com"
	}
	repoURL := baseURL + "/" + parts[0] + "/" + parts[1] + ".git"
	return gitFileRef{RepoURL: repoURL, Path: filepath.Clean(strings.Join(parts[2:], "/")), Ref: version}, true, nil
}

func checkoutGitFileRef(baseDir string, ref gitFileRef) (string, error) {
	cacheRoot := os.Getenv("SPEX_GIT_CACHE_DIR")
	if cacheRoot == "" {
		cacheRoot = filepath.Join(baseDir, ".spex", "git")
	}
	keySource := ref.RepoURL + "@" + ref.Ref
	sum := sha256.Sum256([]byte(keySource))
	cacheDir := filepath.Join(cacheRoot, hex.EncodeToString(sum[:])[:16])
	if _, err := os.Stat(filepath.Join(cacheDir, ".git")); err != nil {
		if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
			return "", err
		}
		if err := runGit("", "clone", "--no-checkout", ref.RepoURL, cacheDir); err != nil {
			return "", err
		}
	}
	if err := runGit(cacheDir, "fetch", "--depth", "1", "origin", ref.Ref); err != nil {
		return "", err
	}
	if err := runGit(cacheDir, "checkout", "--detach", "FETCH_HEAD"); err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, filepath.FromSlash(filepath.ToSlash(ref.Path))), nil
}

func runGit(dir string, args ...string) error {
	_, err := runGitOutput(dir, args...)
	return err
}

func runGitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	capture := newLimitedCommandCapture(maxGitCommandOutputSize)
	cmd.Stdout = capture
	cmd.Stderr = capture
	err := cmd.Run()
	if err != nil {
		message := strings.TrimSpace(capture.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return strings.TrimSpace(capture.String()), nil
}

type limitedCommandCapture struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int64
	truncated bool
}

func newLimitedCommandCapture(limit int64) *limitedCommandCapture {
	return &limitedCommandCapture{limit: limit}
}

func (w *limitedCommandCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.limit - int64(w.buf.Len())
	if remaining <= 0 {
		w.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		w.buf.Write(p[:remaining])
		w.truncated = true
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

func (w *limitedCommandCapture) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.truncated {
		return w.buf.String()
	}
	return w.buf.String() + fmt.Sprintf("\n[spex: command output truncated after %d bytes]\n", w.limit)
}

func expandSuiteScenarioRefs(baseDir string, refs []ScenarioRef, defaultBindingPath, defaultIntegrationProfilePath string) ([]ResolvedScenarioRef, error) {
	seen := map[string]struct{}{}
	var out []ResolvedScenarioRef
	for i, ref := range refs {
		matches, err := expandSuiteScenarioPattern(baseDir, ref.Path)
		if err != nil {
			return nil, err
		}
		bindingPath := defaultBindingPath
		if ref.BindingRef != "" {
			bindingPath, err = resolveSuiteRef(baseDir, ref.BindingRef)
			if err != nil {
				return nil, fmt.Errorf("spec.scenarios[%d].bindingRef: %w", i, err)
			}
			if _, err := os.Stat(bindingPath); err != nil {
				return nil, fmt.Errorf("spec.scenarios[%d].bindingRef: %w", i, err)
			}
		}
		integrationProfilePath := defaultIntegrationProfilePath
		if ref.IntegrationProfileRef != "" {
			integrationProfilePath, err = resolveSuiteRef(baseDir, ref.IntegrationProfileRef)
			if err != nil {
				return nil, fmt.Errorf("spec.scenarios[%d].integrationProfileRef: %w", i, err)
			}
			if _, err := os.Stat(integrationProfilePath); err != nil {
				return nil, fmt.Errorf("spec.scenarios[%d].integrationProfileRef: %w", i, err)
			}
		}
		for _, match := range matches {
			clean := filepath.Clean(match)
			seenKey := scenarioRefKey(clean, bindingPath, integrationProfilePath, ref.Parameters, ref.Tags)
			if _, ok := seen[seenKey]; ok {
				continue
			}
			seen[seenKey] = struct{}{}
			resolved := ResolvedScenarioRef{
				Path:                   clean,
				BindingPath:            bindingPath,
				IntegrationProfilePath: integrationProfilePath,
				Parameters:             copyStringMap(ref.Parameters),
				Tags:                   append([]string{}, ref.Tags...),
			}
			out = append(out, resolved)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func scenarioRefKey(pathValue, bindingPath, integrationProfilePath string, parameters map[string]string, tags []string) string {
	var b strings.Builder
	b.WriteString(pathValue)
	b.WriteByte('\x00')
	b.WriteString(bindingPath)
	b.WriteByte('\x00')
	b.WriteString(integrationProfilePath)
	keys := make([]string, 0, len(parameters))
	for key := range parameters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		b.WriteByte('\x00')
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(parameters[key])
	}
	sortedTags := append([]string{}, tags...)
	sort.Strings(sortedTags)
	for _, tag := range sortedTags {
		b.WriteByte('\x00')
		b.WriteString(tag)
	}
	return b.String()
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func expandSuiteScenarioPattern(baseDir, patternValue string) ([]string, error) {
	patternValue = strings.TrimSpace(patternValue)
	if filepath.IsAbs(patternValue) {
		return expandAbsoluteSuiteScenarioPattern(patternValue)
	}
	return expandAbsoluteSuiteScenarioPattern(filepath.Join(baseDir, patternValue))
}

func expandAbsoluteSuiteScenarioPattern(patternValue string) ([]string, error) {
	if !strings.Contains(patternValue, "**") {
		matches, err := filepath.Glob(patternValue)
		if err != nil {
			return nil, fmt.Errorf("spec.scenarios pattern %q: %w", patternValue, err)
		}
		return matches, nil
	}
	cleanPattern := filepath.Clean(patternValue)
	parts := strings.SplitN(filepath.ToSlash(cleanPattern), "**", 2)
	root := filepath.FromSlash(strings.TrimSuffix(parts[0], "/"))
	if root == "" {
		root = "."
	}
	suffix := strings.TrimPrefix(parts[1], "/")
	var matches []string
	err := filepath.WalkDir(root, func(filePath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		ok, err := path.Match(suffix, relSlash)
		if err != nil {
			return fmt.Errorf("spec.scenarios pattern %q: %w", patternValue, err)
		}
		if !ok && !strings.Contains(suffix, "/") {
			ok, err = path.Match(suffix, path.Base(relSlash))
			if err != nil {
				return fmt.Errorf("spec.scenarios pattern %q: %w", patternValue, err)
			}
		}
		if ok {
			matches = append(matches, filePath)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}

func ValidateIntegrationInputs(in Inputs) error {
	if in.Integration == nil {
		return nil
	}
	kindSpec := in.Integration.Spec.KIND
	if !kindSpec.Start || kindSpec.ClusterName == "" || in.KubeContext == "" {
		return nil
	}
	want := "kind-" + kindSpec.ClusterName
	if in.KubeContext != want {
		return fmt.Errorf("integration profile kind clusterName %q requires kubeContext %q, got %q", kindSpec.ClusterName, want, in.KubeContext)
	}
	return nil
}

func ValidateRuntimeInputs(in Inputs) error {
	if in.Namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	if err := ValidateDNS1123Label("namespace", in.Namespace); err != nil {
		return err
	}
	if err := validateProbeImage("probe image", in.Binding.Spec.Probe.Image); err != nil {
		return err
	}
	if in.Binding.Spec.Probe.ImagePullPolicy != "" {
		switch in.Binding.Spec.Probe.ImagePullPolicy {
		case "Always", "IfNotPresent", "Never":
		default:
			return fmt.Errorf("probe imagePullPolicy must be one of Always, IfNotPresent, or Never")
		}
	}
	if err := ValidateIntegrationInputs(in); err != nil {
		return err
	}
	if err := ValidateLabelValue("run-id", in.RunID); err != nil {
		return err
	}
	return nil
}

func validateProbeImage(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.ContainsAny(value, " \t\r\n\x00") {
		return fmt.Errorf("%s must not contain whitespace or control characters", field)
	}
	return nil
}

func loadYAML[T any](path string) (T, error) {
	var out T
	if err := loadYAMLInto(path, &out); err != nil {
		return out, err
	}
	return out, nil
}

func loadYAMLInto(path string, out any) error {
	content, err := readRegularInputFile(path, maxYAMLInputFileSize)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	if err := decoder.Decode(out); err != nil {
		return err
	}
	return ensureYAMLEOF(decoder)
}

func ensureYAMLEOF(decoder *yaml.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("unexpected trailing YAML document")
}

func readRegularInputFile(path string, maxSize int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", filepath.Base(path))
	}
	if info.Size() > maxSize {
		return nil, fmt.Errorf("%s: file is too large: got %d bytes, max %d bytes", filepath.Base(path), info.Size(), maxSize)
	}
	return os.ReadFile(path)
}

func validateScenario(s Scenario, scenarioPath string) error {
	return validateScenarioWithProviders(s, scenarioPath, nil)
}

func validateScenarioWithProviders(s Scenario, scenarioPath string, providers []Provider) error {
	registry, err := NewProviderRegistryWithProviders(providers)
	if err != nil {
		return err
	}
	if s.APIVersion != "spex.scenario.v0.1" {
		return fmt.Errorf("unsupported apiVersion %q", s.APIVersion)
	}
	if s.Kind != "Scenario" {
		return fmt.Errorf("kind must be Scenario")
	}
	if !idPattern.MatchString(s.Metadata.Name) {
		return fmt.Errorf("metadata.name must match %s", idPattern.String())
	}
	for i, tag := range s.Metadata.Tags {
		if err := validateTag(tag); err != nil {
			return fmt.Errorf("metadata.tags[%d]: %w", i, err)
		}
	}
	if len(s.Spec.Operations) == 0 {
		return fmt.Errorf("spec.operations must contain at least one operation")
	}
	for name, parameter := range s.Spec.Parameters {
		if !parameterNamePattern.MatchString(name) {
			return fmt.Errorf("spec.parameters.%s has invalid name: must match %s", name, parameterNamePattern.String())
		}
		if parameter.Type != "string" {
			return fmt.Errorf("spec.parameters.%s.type must be string", name)
		}
		if err := validateParameterValue("spec.parameters."+name+".default", parameter.Default); err != nil {
			return err
		}
	}
	if err := validateOptionalTimeout("spec.defaults.timeout", s.Spec.Defaults.Timeout); err != nil {
		return err
	}
	if err := validateOptionalDuration("spec.defaults.pollInterval", s.Spec.Defaults.PollInterval); err != nil {
		return err
	}
	if err := validateScenarioTemplates(s); err != nil {
		return err
	}
	if err := validateGraphQLQueryFiles(s); err != nil {
		return err
	}

	operationSlugs := map[string]string{}
	operationIDs := map[string]struct{}{}
	for _, op := range s.Spec.Operations {
		if !idPattern.MatchString(op.ID) {
			return fmt.Errorf("operation %q has invalid id", op.ID)
		}
		if _, ok := operationIDs[op.ID]; ok {
			return fmt.Errorf("name_collision: duplicate operation id %q", op.ID)
		}
		slug := DNSLabel(op.ID)
		if previous, ok := operationSlugs[slug]; ok && previous != op.ID {
			return fmt.Errorf("name_collision: operation ids %q and %q normalize to %q", previous, op.ID, slug)
		}
		operationSlugs[slug] = op.ID
		operationIDs[op.ID] = struct{}{}

		switch op.Type {
		case "mqtt.publish":
			if err := validateOperationBlocks(op, "mqtt"); err != nil {
				return err
			}
			if op.MQTT == nil {
				return fmt.Errorf("operation %q missing mqtt block", op.ID)
			}
			if strings.TrimSpace(op.MQTT.Topic) == "" {
				return fmt.Errorf("operation %q mqtt.topic is required", op.ID)
			}
			if err := validateMQTTTopicTemplate("operation "+op.ID+" mqtt.topic", op.MQTT.Topic); err != nil {
				return err
			}
			if op.MQTT.PayloadTemplateRef == "" {
				return fmt.Errorf("operation %q mqtt.payloadTemplateRef is required", op.ID)
			}
			template, ok := s.Spec.PayloadTemplates[op.MQTT.PayloadTemplateRef]
			if !ok {
				return fmt.Errorf("operation %q references unknown payload template %q", op.ID, op.MQTT.PayloadTemplateRef)
			}
			if strings.TrimSpace(op.MQTT.CorrelationID) == "" {
				return fmt.Errorf("operation %q mqtt.correlationId is required", op.ID)
			}
			if err := validateCorrelationID("operation "+op.ID+" mqtt.correlationId", op.MQTT.CorrelationID); err != nil {
				return err
			}
			if err := validateMQTTPayloadCorrelation(op.ID, template.Body); err != nil {
				return err
			}
		case "mqtt.roundtrip":
			if err := validateOperationBlocks(op, "mqtt"); err != nil {
				return err
			}
			if op.MQTT == nil {
				return fmt.Errorf("operation %q missing mqtt block", op.ID)
			}
			if strings.TrimSpace(op.MQTT.Topic) == "" {
				return fmt.Errorf("operation %q mqtt.topic is required", op.ID)
			}
			if err := validateMQTTTopicTemplate("operation "+op.ID+" mqtt.topic", op.MQTT.Topic); err != nil {
				return err
			}
			if op.MQTT.PayloadTemplateRef == "" {
				return fmt.Errorf("operation %q mqtt.payloadTemplateRef is required", op.ID)
			}
			template, ok := s.Spec.PayloadTemplates[op.MQTT.PayloadTemplateRef]
			if !ok {
				return fmt.Errorf("operation %q references unknown payload template %q", op.ID, op.MQTT.PayloadTemplateRef)
			}
			if err := validateOptionalTimeout("operation "+op.ID+" mqtt.timeout", op.MQTT.Timeout); err != nil {
				return err
			}
			if err := validateMQTTRoundTripClientMode("operation "+op.ID+" mqtt.clientMode", op.MQTT.ClientMode); err != nil {
				return err
			}
			if err := validateMatchers("operation "+op.ID+" mqtt.match", op.MQTT.Match); err != nil {
				return err
			}
			if strings.TrimSpace(op.MQTT.CorrelationID) == "" {
				return fmt.Errorf("operation %q mqtt.correlationId is required", op.ID)
			}
			if err := validateCorrelationID("operation "+op.ID+" mqtt.correlationId", op.MQTT.CorrelationID); err != nil {
				return err
			}
			if err := validateMQTTPayloadCorrelation(op.ID, template.Body); err != nil {
				return err
			}
			if err := validateCorrelationMatchers("operation "+op.ID+" mqtt.match", op.MQTT.CorrelationID, op.MQTT.Match); err != nil {
				return err
			}
		case "rabbitmq.publish":
			if err := validateOperationBlocks(op, "rabbitmq"); err != nil {
				return err
			}
			if op.RabbitMQ == nil {
				return fmt.Errorf("operation %q missing rabbitmq block", op.ID)
			}
			if strings.TrimSpace(op.RabbitMQ.RoutingKey) == "" {
				return fmt.Errorf("operation %q rabbitmq.routingKey is required", op.ID)
			}
			if op.RabbitMQ.PayloadTemplateRef == "" {
				return fmt.Errorf("operation %q rabbitmq.payloadTemplateRef is required", op.ID)
			}
			template, ok := s.Spec.PayloadTemplates[op.RabbitMQ.PayloadTemplateRef]
			if !ok {
				return fmt.Errorf("operation %q references unknown payload template %q", op.ID, op.RabbitMQ.PayloadTemplateRef)
			}
			if err := validateCorrelationID("operation "+op.ID+" rabbitmq.correlationId", op.RabbitMQ.CorrelationID); err != nil {
				return err
			}
			if err := validateMQTTPayloadCorrelation(op.ID, template.Body); err != nil {
				return err
			}
		case "rabbitmq.expect":
			if err := validateOperationBlocks(op, "rabbitmq"); err != nil {
				return err
			}
			if op.RabbitMQ == nil {
				return fmt.Errorf("operation %q missing rabbitmq block", op.ID)
			}
			if strings.TrimSpace(op.RabbitMQ.Queue) == "" {
				return fmt.Errorf("operation %q rabbitmq.queue is required", op.ID)
			}
			if err := validateOptionalTimeout("operation "+op.ID+" rabbitmq.timeout", op.RabbitMQ.Timeout); err != nil {
				return err
			}
			if err := validateMatchers("operation "+op.ID+" rabbitmq.match", op.RabbitMQ.Match); err != nil {
				return err
			}
			if err := validateCorrelationID("operation "+op.ID+" rabbitmq.correlationId", op.RabbitMQ.CorrelationID); err != nil {
				return err
			}
			if err := validateCorrelationMatchers("operation "+op.ID+" rabbitmq.match", op.RabbitMQ.CorrelationID, op.RabbitMQ.Match); err != nil {
				return err
			}
		case "redpanda.contains":
			if err := validateOperationBlocks(op, "redpanda"); err != nil {
				return err
			}
			if op.Redpanda == nil {
				return fmt.Errorf("operation %q missing redpanda block", op.ID)
			}
			if op.Redpanda.TopicRef == "" {
				return fmt.Errorf("operation %q redpanda.topicRef is required", op.ID)
			}
			if err := validateOptionalTimeout("operation "+op.ID+" redpanda.timeout", op.Redpanda.Timeout); err != nil {
				return err
			}
			if err := validateMatchers("operation "+op.ID+" redpanda.match", op.Redpanda.Match); err != nil {
				return err
			}
			if err := validateCorrelationID("operation "+op.ID+" redpanda.correlationId", op.Redpanda.CorrelationID); err != nil {
				return err
			}
			if err := validateCorrelationMatchers("operation "+op.ID+" redpanda.match", op.Redpanda.CorrelationID, op.Redpanda.Match); err != nil {
				return err
			}
		case "graphql.expect":
			if err := validateOperationBlocks(op, "graphql"); err != nil {
				return err
			}
			if op.GraphQL == nil {
				return fmt.Errorf("operation %q missing graphql block", op.ID)
			}
			if op.GraphQL.QueryRef == "" {
				return fmt.Errorf("operation %q graphql.queryRef is required", op.ID)
			}
			if err := validateOptionalTimeout("operation "+op.ID+" graphql.timeout", op.GraphQL.Timeout); err != nil {
				return err
			}
			if _, ok := s.Spec.GraphQLQueries[op.GraphQL.QueryRef]; !ok {
				return fmt.Errorf("operation %q references unknown graphql query %q", op.ID, op.GraphQL.QueryRef)
			}
			query := s.Spec.GraphQLQueries[op.GraphQL.QueryRef]
			if err := validateGraphQLQueryContract(resolveScenarioFile(scenarioPath, query.File)); err != nil {
				return fmt.Errorf("operation %q graphql_query_contract_failure: %w", op.ID, err)
			}
			if err := validateMatchers("operation "+op.ID+" graphql.match", op.GraphQL.Match); err != nil {
				return err
			}
			if err := validateGraphQLCorrelation("operation "+op.ID+" graphql", op.GraphQL); err != nil {
				return err
			}
		case "mongodb.expect":
			if err := validateOperationBlocks(op, "mongodb"); err != nil {
				return err
			}
			if op.MongoDB == nil {
				return fmt.Errorf("operation %q missing mongodb block", op.ID)
			}
			if strings.TrimSpace(op.MongoDB.Collection) == "" {
				return fmt.Errorf("operation %q mongodb.collection is required", op.ID)
			}
			if strings.ContainsAny(op.MongoDB.Collection, "\x00\r\n\t") {
				return fmt.Errorf("operation %q mongodb.collection must not contain control characters", op.ID)
			}
			if strings.TrimSpace(op.MongoDB.Filter) == "" {
				return fmt.Errorf("operation %q mongodb.filter is required", op.ID)
			}
			if err := validatePayloadTemplateJSON("operation "+op.ID+" mongodb.filter", op.MongoDB.Filter); err != nil {
				return err
			}
			if err := validateOptionalTimeout("operation "+op.ID+" mongodb.timeout", op.MongoDB.Timeout); err != nil {
				return err
			}
			if err := validateMatchers("operation "+op.ID+" mongodb.match", op.MongoDB.Match); err != nil {
				return err
			}
			if err := validateCorrelationID("operation "+op.ID+" mongodb.correlationId", op.MongoDB.CorrelationID); err != nil {
				return err
			}
			if err := validateCorrelationMatchers("operation "+op.ID+" mongodb.match", op.MongoDB.CorrelationID, op.MongoDB.Match); err != nil {
				return err
			}
		case "postgresql.expect":
			if err := validateOperationBlocks(op, "postgresql"); err != nil {
				return err
			}
			if op.Postgres == nil {
				return fmt.Errorf("operation %q missing postgresql block", op.ID)
			}
			if strings.TrimSpace(op.Postgres.Query) == "" {
				return fmt.Errorf("operation %q postgresql.query is required", op.ID)
			}
			if strings.TrimSpace(op.Postgres.CorrelationID) == "" {
				return fmt.Errorf("operation %q postgresql.correlationId is required", op.ID)
			}
			if err := validateOptionalTimeout("operation "+op.ID+" postgresql.timeout", op.Postgres.Timeout); err != nil {
				return err
			}
			if err := validateMatchers("operation "+op.ID+" postgresql.match", op.Postgres.Match); err != nil {
				return err
			}
			if err := validateCorrelationID("operation "+op.ID+" postgresql.correlationId", op.Postgres.CorrelationID); err != nil {
				return err
			}
			if err := validateCorrelationMatchers("operation "+op.ID+" postgresql.match", op.Postgres.CorrelationID, op.Postgres.Match); err != nil {
				return err
			}
		default:
			if err := validateGenericProviderOperation(op, registry); err != nil {
				return err
			}
		}
	}
	for _, op := range s.Spec.Operations {
		dependencies := op.DependsOn
		if op.After != "" {
			dependencies = append([]string{op.After}, dependencies...)
		}
		for _, dependency := range dependencies {
			if _, ok := operationIDs[dependency]; !ok {
				return fmt.Errorf("operation %q depends on unknown operation %q", op.ID, dependency)
			}
		}
	}
	seenOperations := map[string]struct{}{}
	for _, op := range s.Spec.Operations {
		dependencies := op.DependsOn
		if op.After != "" {
			dependencies = append([]string{op.After}, dependencies...)
		}
		for _, dependency := range dependencies {
			if _, ok := seenOperations[dependency]; !ok {
				return fmt.Errorf("operation %q depends on %q, but dependencies must appear earlier in spec.operations", op.ID, dependency)
			}
		}
		seenOperations[op.ID] = struct{}{}
	}
	return nil
}

func validateGenericProviderOperation(op Operation, registry *ProviderRegistry) error {
	capability, ok := registry.ResolveCapability(op.Type)
	if !ok {
		return fmt.Errorf("operation %q uses unsupported type %q", op.ID, op.Type)
	}
	if err := validateOperationBlocks(op, ""); err != nil {
		return err
	}
	if len(op.With) == 0 {
		return fmt.Errorf("operation %q with is required for provider operation type %q", op.ID, op.Type)
	}
	if err := validateOptionalTimeout("operation "+op.ID+" timeout", op.Timeout); err != nil {
		return err
	}
	generic := GenericOperation{
		ID:      op.ID,
		Type:    op.Type,
		With:    copyAnyMap(op.With),
		Timeout: op.Timeout,
	}
	if _, ok := generic.With[bindingRefKey]; !ok {
		generic.With[bindingRefKey] = legacyBindingName(capability.Provider)
	}
	if err := ValidateOperationInput(generic, capability.Capability); err != nil {
		return err
	}
	return nil
}

func validateOperationBlocks(op Operation, expected string) error {
	if expected != "mqtt" && op.MQTT != nil {
		return fmt.Errorf("operation %q of type %q must not contain mqtt block", op.ID, op.Type)
	}
	if expected != "redpanda" && op.Redpanda != nil {
		return fmt.Errorf("operation %q of type %q must not contain redpanda block", op.ID, op.Type)
	}
	if expected != "graphql" && op.GraphQL != nil {
		return fmt.Errorf("operation %q of type %q must not contain graphql block", op.ID, op.Type)
	}
	if expected != "mongodb" && op.MongoDB != nil {
		return fmt.Errorf("operation %q of type %q must not contain mongodb block", op.ID, op.Type)
	}
	if expected != "postgresql" && op.Postgres != nil {
		return fmt.Errorf("operation %q of type %q must not contain postgresql block", op.ID, op.Type)
	}
	if expected != "rabbitmq" && op.RabbitMQ != nil {
		return fmt.Errorf("operation %q of type %q must not contain rabbitmq block", op.ID, op.Type)
	}
	return nil
}

func validateIntegrationProfile(p IntegrationProfile) error {
	if p.APIVersion != "spex.integration.v0.1" {
		return fmt.Errorf("unsupported apiVersion %q", p.APIVersion)
	}
	if p.Kind != "IntegrationProfile" {
		return fmt.Errorf("kind must be IntegrationProfile")
	}
	if p.Spec.KIND.ClusterName != "" {
		if err := ValidateDNS1123Label("spec.kind.clusterName", p.Spec.KIND.ClusterName); err != nil {
			return err
		}
	}
	if p.Spec.KIND.Config != "" {
		if _, err := os.Stat(p.Spec.KIND.Config); err != nil {
			return fmt.Errorf("spec.kind.config: %w", err)
		}
	}
	for i, app := range p.Spec.HelmApps {
		if !idPattern.MatchString(app.Name) {
			return fmt.Errorf("spec.helmApps[%d].name must match %s", i, idPattern.String())
		}
		if strings.TrimSpace(app.Chart) == "" {
			return fmt.Errorf("spec.helmApps[%d].chart is required", i)
		}
		if app.Namespace != "" {
			if err := ValidateDNS1123Label(fmt.Sprintf("spec.helmApps[%d].namespace", i), app.Namespace); err != nil {
				return err
			}
		}
		if err := validateOptionalDuration(fmt.Sprintf("spec.helmApps[%d].timeout", i), app.Timeout); err != nil {
			return err
		}
		values := append([]string{app.Chart, app.Repo}, app.Values...)
		for j, value := range values {
			if err := validateIntegrationPlaceholders(fmt.Sprintf("spec.helmApps[%d] value %d", i, j), value); err != nil {
				return err
			}
			if err := validateIntegrationCommandSecurity(fmt.Sprintf("spec.helmApps[%d] value %d", i, j), value); err != nil {
				return err
			}
		}
		for key, value := range app.Set {
			if strings.TrimSpace(key) == "" {
				return fmt.Errorf("spec.helmApps[%d].set contains empty key", i)
			}
			if err := validateIntegrationCommandSecurity(fmt.Sprintf("spec.helmApps[%d].set.%s", i, key), value); err != nil {
				return err
			}
		}
	}
	for i, command := range append(p.Spec.KIND.Commands, p.Spec.Setup.Commands...) {
		if strings.TrimSpace(command.Command) == "" {
			return fmt.Errorf("integration command %d is empty", i)
		}
		if command.Timeout < 0 {
			return fmt.Errorf("integration command %d timeout must not be negative", i)
		}
		if err := validateIntegrationPlaceholders(fmt.Sprintf("integration command %d", i), command.Command); err != nil {
			return err
		}
		if err := validateIntegrationCommandSecurity(fmt.Sprintf("integration command %d", i), command.Command); err != nil {
			return err
		}
		if !p.Spec.AllowFakes && fakeServicePattern.MatchString(command.Command) {
			return fmt.Errorf("integration command %d references a fake/mock service; set spec.allowFakes: true if the profile intentionally provisions one", i)
		}
	}
	for i, container := range p.Spec.KIND.Containers {
		if strings.TrimSpace(container) == "" {
			return fmt.Errorf("spec.kind.containers[%d] is empty", i)
		}
		if err := validateIntegrationPlaceholders(fmt.Sprintf("spec.kind.containers[%d]", i), container); err != nil {
			return err
		}
	}
	return nil
}

func validateIntegrationCommandSecurity(field, command string) error {
	if secretLiteralPattern.MatchString(command) {
		return fmt.Errorf("%s contains a literal secret value; use an environment variable reference instead", field)
	}
	if commandURLUserinfoPattern.MatchString(command) {
		return fmt.Errorf("%s must not contain URL userinfo or embedded credentials", field)
	}
	return nil
}

func validateIntegrationPlaceholders(field, value string) error {
	for _, match := range templateRefPattern.FindAllStringSubmatch(value, -1) {
		ref := match[1]
		if _, ok := integrationPlaceholders[ref]; ok {
			continue
		}
		return fmt.Errorf("%s contains unsupported integration placeholder %q", field, ref)
	}
	return nil
}

func validateGraphQLQueryFiles(s Scenario) error {
	basenames := map[string]string{}
	for ref, query := range s.Spec.GraphQLQueries {
		if !idPattern.MatchString(ref) {
			return fmt.Errorf("spec.graphqlQueries contains invalid ref %q: must match %s", ref, idPattern.String())
		}
		if query.File == "" {
			return fmt.Errorf("spec.graphqlQueries.%s.file is required", ref)
		}
		base := filepath.Base(query.File)
		if base == "." || base == string(filepath.Separator) {
			return fmt.Errorf("spec.graphqlQueries.%s.file basename %q must be a valid ConfigMap data key", ref, base)
		}
		if err := validateDataKey("spec.graphqlQueries."+ref+".file basename", base); err != nil {
			return err
		}
		if previous, ok := basenames[base]; ok && previous != ref {
			return fmt.Errorf("name_collision: graphql query refs %q and %q use the same generated ConfigMap key %q", previous, ref, base)
		}
		basenames[base] = ref
	}
	return nil
}

func validateMatchers(field string, matchers []Matcher) error {
	if len(matchers) == 0 {
		return fmt.Errorf("%s must contain at least one matcher", field)
	}
	for i, matcher := range matchers {
		item := fmt.Sprintf("%s[%d]", field, i)
		if !matcherPathPattern.MatchString(matcher.Path) {
			return fmt.Errorf("%s.path has unsupported syntax", item)
		}
		expectationCount := 0
		if matcher.EqualsString != "" {
			expectationCount++
		}
		if matcher.EqualsNumber != "" {
			expectationCount++
			if _, ok := new(big.Rat).SetString(matcher.EqualsNumber); !ok {
				return fmt.Errorf("%s.equalsNumber must be a JSON number literal", item)
			}
		}
		if matcher.EqualsBool != nil {
			expectationCount++
		}
		if matcher.EqualsNull != nil {
			expectationCount++
			if !*matcher.EqualsNull {
				return fmt.Errorf("%s.equalsNull must be true when specified", item)
			}
		}
		if expectationCount != 1 {
			return fmt.Errorf("%s must specify exactly one expectation", item)
		}
	}
	return nil
}

func validateGraphQLCorrelation(field string, graphql *GraphQLExpectation) error {
	for name := range graphql.Variables {
		if !graphQLNamePattern.MatchString(name) {
			return fmt.Errorf("%s.variables contains invalid GraphQL variable name %q", field, name)
		}
	}
	if graphql.Variables["scenarioRunId"] != "${scenarioRunId}" {
		return fmt.Errorf("%s.variables.scenarioRunId must be ${scenarioRunId}", field)
	}
	correlationID := graphql.Variables["correlationId"]
	if correlationID == "" {
		return fmt.Errorf("%s.variables.correlationId is required", field)
	}
	if err := validateCorrelationID(field+".variables.correlationId", correlationID); err != nil {
		return err
	}
	return validateCorrelationMatchers(field+".match", correlationID, graphql.Match)
}

func validateCorrelationID(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not contain leading or trailing whitespace", field)
	}
	if strings.ContainsAny(value, "\x00\r\n\t") {
		return fmt.Errorf("%s must not contain control characters", field)
	}
	if templateRefPattern.MatchString(value) {
		return fmt.Errorf("%s must not contain template expressions", field)
	}
	return nil
}

func validateCorrelationMatchers(field, correlationID string, matchers []Matcher) error {
	hasScenarioRunID := false
	hasCorrelationID := false
	for _, matcher := range matchers {
		if matcher.EqualsString == "${scenarioRunId}" {
			hasScenarioRunID = true
		}
		if correlationID != "" && matcher.EqualsString == correlationID {
			hasCorrelationID = true
		}
	}
	if !hasScenarioRunID {
		return fmt.Errorf("%s must include an equalsString matcher for ${scenarioRunId}", field)
	}
	if correlationID == "" {
		return fmt.Errorf("%s correlationId is required", field)
	}
	if !hasCorrelationID {
		return fmt.Errorf("%s must include an equalsString matcher for correlationId %q", field, correlationID)
	}
	return nil
}

func validateMQTTPayloadCorrelation(operationID, body string) error {
	if !strings.Contains(body, "${scenarioRunId}") {
		return fmt.Errorf("operation %q mqtt payload template must include ${scenarioRunId}", operationID)
	}
	if !strings.Contains(body, "${correlationId}") {
		return fmt.Errorf("operation %q mqtt payload template must include ${correlationId}", operationID)
	}
	return nil
}

func validateScenarioTemplates(s Scenario) error {
	params := map[string]struct{}{}
	for name := range s.Spec.Parameters {
		params[name] = struct{}{}
	}
	for name, template := range s.Spec.PayloadTemplates {
		if !idPattern.MatchString(name) {
			return fmt.Errorf("spec.payloadTemplates contains invalid ref %q: must match %s", name, idPattern.String())
		}
		if template.ContentType != "application/json" {
			return fmt.Errorf("payloadTemplates.%s.contentType must be application/json in this release", name)
		}
		if err := validateTemplateString("payloadTemplates."+name+".body", template.Body, params); err != nil {
			return err
		}
		if err := validatePayloadTemplateJSON("payloadTemplates."+name+".body", template.Body); err != nil {
			return err
		}
	}
	for _, op := range s.Spec.Operations {
		switch op.Type {
		case "mqtt.publish":
			if op.MQTT != nil {
				if err := validateTemplateString("operation "+op.ID+" mqtt.topic", op.MQTT.Topic, params); err != nil {
					return err
				}
			}
		case "mqtt.roundtrip":
			if op.MQTT != nil {
				if err := validateTemplateString("operation "+op.ID+" mqtt.topic", op.MQTT.Topic, params); err != nil {
					return err
				}
				for i, matcher := range op.MQTT.Match {
					if err := validateMatcherTemplate("operation "+op.ID+" mqtt.match["+fmt.Sprint(i)+"]", matcher, params); err != nil {
						return err
					}
				}
			}
		case "redpanda.contains":
			if op.Redpanda != nil {
				for i, matcher := range op.Redpanda.Match {
					if err := validateMatcherTemplate("operation "+op.ID+" redpanda.match["+fmt.Sprint(i)+"]", matcher, params); err != nil {
						return err
					}
				}
			}
		case "graphql.expect":
			if op.GraphQL != nil {
				for name, value := range op.GraphQL.Variables {
					if err := validateTemplateString("operation "+op.ID+" graphql.variables."+name, value, params); err != nil {
						return err
					}
				}
				for i, matcher := range op.GraphQL.Match {
					if err := validateMatcherTemplate("operation "+op.ID+" graphql.match["+fmt.Sprint(i)+"]", matcher, params); err != nil {
						return err
					}
				}
			}
		case "mongodb.expect":
			if op.MongoDB != nil {
				if err := validateTemplateString("operation "+op.ID+" mongodb.filter", op.MongoDB.Filter, params); err != nil {
					return err
				}
				for i, matcher := range op.MongoDB.Match {
					if err := validateMatcherTemplate("operation "+op.ID+" mongodb.match["+fmt.Sprint(i)+"]", matcher, params); err != nil {
						return err
					}
				}
			}
		case "postgresql.expect":
			if op.Postgres != nil {
				if err := validateTemplateString("operation "+op.ID+" postgresql.query", op.Postgres.Query, params); err != nil {
					return err
				}
				for i, arg := range op.Postgres.Args {
					if err := validateTemplateString("operation "+op.ID+" postgresql.args["+fmt.Sprint(i)+"]", arg, params); err != nil {
						return err
					}
				}
				for i, matcher := range op.Postgres.Match {
					if err := validateMatcherTemplate("operation "+op.ID+" postgresql.match["+fmt.Sprint(i)+"]", matcher, params); err != nil {
						return err
					}
				}
			}
		case "rabbitmq.publish":
			if op.RabbitMQ != nil {
				if err := validateTemplateString("operation "+op.ID+" rabbitmq.exchange", op.RabbitMQ.Exchange, params); err != nil {
					return err
				}
				if err := validateTemplateString("operation "+op.ID+" rabbitmq.routingKey", op.RabbitMQ.RoutingKey, params); err != nil {
					return err
				}
			}
		case "rabbitmq.expect":
			if op.RabbitMQ != nil {
				if err := validateTemplateString("operation "+op.ID+" rabbitmq.queue", op.RabbitMQ.Queue, params); err != nil {
					return err
				}
				for i, matcher := range op.RabbitMQ.Match {
					if err := validateMatcherTemplate("operation "+op.ID+" rabbitmq.match["+fmt.Sprint(i)+"]", matcher, params); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func validateMatcherTemplate(field string, matcher Matcher, params map[string]struct{}) error {
	for suffix, value := range map[string]string{
		".path":         matcher.Path,
		".equalsString": matcher.EqualsString,
		".equalsNumber": matcher.EqualsNumber,
	} {
		if err := validateTemplateString(field+suffix, value, params); err != nil {
			return err
		}
	}
	return nil
}

func validateTemplateString(field, value string, params map[string]struct{}) error {
	for _, match := range templateRefPattern.FindAllStringSubmatch(value, -1) {
		ref := match[1]
		switch {
		case ref == "scenarioRunId" || ref == "correlationId":
			continue
		case strings.HasPrefix(ref, "param."):
			name := strings.TrimPrefix(ref, "param.")
			if _, ok := params[name]; ok {
				continue
			}
			return fmt.Errorf("%s references unknown parameter %q", field, name)
		default:
			return fmt.Errorf("%s contains unsupported template reference %q", field, ref)
		}
	}
	return nil
}

func validatePayloadTemplateJSON(field, body string) error {
	var parsed any
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return fmt.Errorf("%s must be valid JSON: %w", field, err)
	}
	return validateJSONTemplateValue(field, parsed)
}

func validateJSONTemplateValue(field string, value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if err := validateJSONTemplateValue(field+"."+key, child); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range typed {
			if err := validateJSONTemplateValue(fmt.Sprintf("%s[%d]", field, i), child); err != nil {
				return err
			}
		}
	case string:
		matches := templateRefPattern.FindAllString(typed, -1)
		if len(matches) > 0 && (len(matches) != 1 || typed != matches[0]) {
			return fmt.Errorf("%s contains template placeholder embedded inside a larger JSON string value", field)
		}
	}
	return nil
}

func validateOptionalDuration(field, value string) error {
	if value == "" {
		return nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s must be a Go duration: %w", field, err)
	}
	if duration <= 0 {
		return fmt.Errorf("%s must be positive", field)
	}
	return nil
}

func validateOptionalTimeout(field, value string) error {
	if err := validateOptionalDuration(field, value); err != nil {
		return err
	}
	if value == "" {
		return nil
	}
	duration, _ := time.ParseDuration(value)
	if duration < time.Second {
		return fmt.Errorf("%s must be at least 1s", field)
	}
	return nil
}

func validateMQTTRoundTripClientMode(field, value string) error {
	switch value {
	case "", "separate", "shared":
		return nil
	default:
		return fmt.Errorf("%s must be separate or shared", field)
	}
}

func resolveScenarioFile(scenarioPath, ref string) string {
	if ref == "" || filepath.IsAbs(ref) {
		return ref
	}
	scenarioRelative := filepath.Join(filepath.Dir(scenarioPath), ref)
	if _, err := os.Stat(scenarioRelative); err == nil {
		return scenarioRelative
	}
	return ref
}

func validateGraphQLQueryContract(path string) error {
	if path == "" {
		return fmt.Errorf("query file is required")
	}
	content, err := readRegularInputFile(path, maxGraphQLQueryFileSize)
	if err != nil {
		return err
	}
	body := stripGraphQLComments(string(content))
	signatureEnd := strings.Index(body, "{")
	if signatureEnd < 0 {
		return fmt.Errorf("query body must contain selection set")
	}
	executable := body[signatureEnd:]
	for _, variable := range []string{"$scenarioRunId", "$correlationId"} {
		if !strings.Contains(executable, variable) {
			return fmt.Errorf("query must use %s after the operation signature", variable)
		}
	}
	return nil
}

func stripGraphQLComments(body string) string {
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		lines = append(lines, stripGraphQLCommentLine(line))
	}
	return strings.Join(lines, "\n")
}

func stripGraphQLCommentLine(line string) string {
	inString := false
	escaped := false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && inString {
			escaped = true
			continue
		}
		if r == '"' {
			inString = !inString
			continue
		}
		if r == '#' && !inString {
			return line[:i]
		}
	}
	return line
}

func validateBinding(b TargetBinding) error {
	if b.APIVersion != "spex.binding.v0.1" {
		return fmt.Errorf("unsupported apiVersion %q", b.APIVersion)
	}
	if b.Kind != "TargetBinding" {
		return fmt.Errorf("kind must be TargetBinding")
	}
	if !idPattern.MatchString(b.Metadata.Name) {
		return fmt.Errorf("metadata.name must match %s", idPattern.String())
	}
	if b.Spec.Namespace == "" {
		return fmt.Errorf("spec.namespace is required")
	}
	if err := ValidateDNS1123Label("spec.namespace", b.Spec.Namespace); err != nil {
		return err
	}
	if b.Spec.Probe.ServiceAccountName != "" {
		if err := ValidateDNS1123Subdomain("spec.probe.serviceAccountName", b.Spec.Probe.ServiceAccountName); err != nil {
			return err
		}
	}
	if !b.Spec.RBAC.Create && b.Spec.Probe.ServiceAccountName == "" {
		return fmt.Errorf("spec.probe.serviceAccountName is required when spec.rbac.create is false")
	}
	if b.Spec.Probe.ImagePullPolicy != "" {
		switch b.Spec.Probe.ImagePullPolicy {
		case "Always", "IfNotPresent", "Never":
		default:
			return fmt.Errorf("spec.probe.imagePullPolicy must be one of Always, IfNotPresent, or Never")
		}
	}
	for id, secret := range b.Spec.Secrets {
		if !idPattern.MatchString(id) {
			return fmt.Errorf("spec.secrets contains invalid id %q: must match %s", id, idPattern.String())
		}
		switch secret.Type {
		case "kubernetesSecret", "localEnvFile", "awsSsmParameter":
		default:
			return fmt.Errorf("secret %q uses unsupported type %q", id, secret.Type)
		}
		if secret.Name == "" {
			return fmt.Errorf("secret %q requires name", id)
		}
		if err := ValidateDNS1123Subdomain("spec.secrets."+id+".name", secret.Name); err != nil {
			return err
		}
		if len(secret.Keys) == 0 {
			return fmt.Errorf("spec.secrets.%s.keys must contain at least one key mapping", id)
		}
		for logicalKey, kubernetesKey := range secret.Keys {
			if !parameterNamePattern.MatchString(logicalKey) {
				return fmt.Errorf("spec.secrets.%s.keys contains invalid logical key %q: must match %s", id, logicalKey, parameterNamePattern.String())
			}
			if err := validateDataKey("spec.secrets."+id+".keys."+logicalKey, kubernetesKey); err != nil {
				return err
			}
		}
		switch secret.Type {
		case "localEnvFile":
			if strings.TrimSpace(secret.EnvFile) == "" {
				return fmt.Errorf("spec.secrets.%s.envFile is required for localEnvFile", id)
			}
			for logicalKey := range secret.Keys {
				envName := secret.Env[logicalKey]
				if envName == "" {
					envName = defaultSecretEnvName(id, logicalKey)
				}
				if !parameterNamePattern.MatchString(envName) {
					return fmt.Errorf("spec.secrets.%s.env.%s must be a valid environment variable name", id, logicalKey)
				}
			}
		case "awsSsmParameter":
			for logicalKey := range secret.Keys {
				if ssmParameterName(secret.SSMParameters[logicalKey]) == "" {
					return fmt.Errorf("spec.secrets.%s.ssmParameters.%s is required for awsSsmParameter", id, logicalKey)
				}
			}
		}
	}
	if err := validateSecretRef(b, "spec.mqtt.credentialsRef", b.Spec.MQTT.CredentialsRef, []string{"username", "password"}); err != nil {
		return err
	}
	if b.Spec.MQTT.ClientIDPrefix != "" && !idPattern.MatchString(b.Spec.MQTT.ClientIDPrefix) {
		return fmt.Errorf("spec.mqtt.clientIdPrefix must match %s", idPattern.String())
	}
	if b.Spec.MQTT.BrokerURL != "" && !isSSMReference(b.Spec.MQTT.BrokerURL) {
		if err := validateURLNoCredentials("spec.mqtt.brokerURL", b.Spec.MQTT.BrokerURL, []string{"tcp", "ssl", "ws", "wss", "mqtt", "mqtts"}); err != nil {
			return err
		}
	}
	if err := validateURLNoCredentials("spec.rabbitmq.uri", b.Spec.RabbitMQ.URI, []string{"amqp", "amqps"}); err != nil {
		return err
	}
	if err := validateSecretRef(b, "spec.rabbitmq.credentialsRef", b.Spec.RabbitMQ.CredentialsRef, []string{"username", "password"}); err != nil {
		return err
	}
	if err := validateURLNoCredentials("spec.graphql.endpoint", b.Spec.GraphQL.Endpoint, []string{"http", "https"}); err != nil {
		return err
	}
	if err := validateURLNoCredentials("spec.mongodb.uri", b.Spec.MongoDB.URI, []string{"mongodb", "mongodb+srv"}); err != nil {
		return err
	}
	switch b.Spec.MongoDB.Deployment {
	case "", "selfManaged", "atlas":
	default:
		return fmt.Errorf("spec.mongodb.deployment must be selfManaged or atlas")
	}
	if b.Spec.MongoDB.Database != "" && strings.ContainsAny(b.Spec.MongoDB.Database, " \t\r\n\x00/\\.") {
		return fmt.Errorf("spec.mongodb.database must not contain whitespace, control characters, slash, backslash, or dot")
	}
	if err := validateSecretRef(b, "spec.mongodb.credentialsRef", b.Spec.MongoDB.CredentialsRef, []string{"username", "password"}); err != nil {
		return err
	}
	if b.Spec.MongoDB.Deployment == "atlas" && b.Spec.MongoDB.CredentialsRef == "" {
		return fmt.Errorf("spec.mongodb.credentialsRef is required when spec.mongodb.deployment is atlas")
	}
	if err := validateURLNoCredentials("spec.postgresql.uri", b.Spec.PostgreSQL.URI, []string{"postgres", "postgresql"}); err != nil {
		return err
	}
	if err := validateSecretRef(b, "spec.postgresql.credentialsRef", b.Spec.PostgreSQL.CredentialsRef, []string{"username", "password"}); err != nil {
		return err
	}
	if err := validateRedpandaBrokers("spec.redpanda.brokers", b.Spec.Redpanda.Brokers); err != nil {
		return err
	}
	for ref, topic := range b.Spec.Redpanda.Topics {
		if !idPattern.MatchString(ref) {
			return fmt.Errorf("spec.redpanda.topics contains invalid topic ref %q: must match %s", ref, idPattern.String())
		}
		if err := validateKafkaTopicName("spec.redpanda.topics."+ref+".name", topic.Name); err != nil {
			return err
		}
	}
	if err := validateGenericBindings(b); err != nil {
		return err
	}
	return nil
}

func validateGenericBindings(b TargetBinding) error {
	seen := map[string]struct{}{}
	for i, binding := range b.Spec.Bindings {
		field := fmt.Sprintf("spec.bindings[%d]", i)
		if binding.Name == "" {
			return fmt.Errorf("%s.name is required", field)
		}
		if !idPattern.MatchString(binding.Name) {
			return fmt.Errorf("%s.name must match %s", field, idPattern.String())
		}
		if _, ok := seen[binding.Name]; ok {
			return fmt.Errorf("name_collision: duplicate binding name %q", binding.Name)
		}
		seen[binding.Name] = struct{}{}
		if binding.Kind == "" {
			return fmt.Errorf("%s.kind is required", field)
		}
		switch binding.Kind {
		case "influxdb.connection":
			version, _ := binding.With["version"].(string)
			switch version {
			case "v2", "v3":
			default:
				return fmt.Errorf("%s.with.version must be v2 or v3", field)
			}
			endpoint, _ := binding.With["endpoint"].(string)
			if err := validateURLNoCredentials(field+".with.endpoint", endpoint, []string{"http", "https"}); err != nil {
				return err
			}
			org, _ := binding.With["org"].(string)
			database, _ := binding.With["database"].(string)
			if version == "v2" && org == "" {
				return fmt.Errorf("%s.with.org is required for InfluxDB v2", field)
			}
			if version == "v3" && database == "" {
				return fmt.Errorf("%s.with.database is required for InfluxDB v3", field)
			}
			credentialsRef, _ := binding.With["credentialsRef"].(string)
			if err := validateSecretRef(b, field+".with.credentialsRef", credentialsRef, []string{"token"}); err != nil {
				return err
			}
		case "redis.connection":
			uri, _ := binding.With["uri"].(string)
			if err := validateURLNoCredentials(field+".with.uri", uri, []string{"redis"}); err != nil {
				return err
			}
			credentialsRef, _ := binding.With["credentialsRef"].(string)
			if err := validateSecretRef(b, field+".with.credentialsRef", credentialsRef, []string{"username", "password"}); err != nil {
				return err
			}
		default:
			if !operationTypePattern.MatchString(binding.Kind) {
				return fmt.Errorf("%s.kind must be provider-qualified", field)
			}
		}
	}
	return nil
}

func validateGraphQLAuth(b TargetBinding) error {
	authType := b.Spec.GraphQL.Auth.Type
	if authType == "" {
		authType = "bearerToken"
	}
	switch authType {
	case "bearerToken":
		return validateRequiredSecretRef(b, "spec.graphql.credentialsRef", b.Spec.GraphQL.CredentialsRef, []string{"token"})
	case "keycloakClientCredentials":
		if b.Spec.GraphQL.Auth.TokenURL == "" {
			return fmt.Errorf("binding_validation_failure: spec.graphql.auth.tokenURL is required for keycloakClientCredentials")
		}
		if err := validateURLNoCredentials("spec.graphql.auth.tokenURL", b.Spec.GraphQL.Auth.TokenURL, []string{"http", "https"}); err != nil {
			return err
		}
		if b.Spec.GraphQL.Auth.ClientID == "" {
			return fmt.Errorf("binding_validation_failure: spec.graphql.auth.clientID is required for keycloakClientCredentials")
		}
		if strings.ContainsAny(b.Spec.GraphQL.Auth.ClientID, " \t\r\n\x00") {
			return fmt.Errorf("binding_validation_failure: spec.graphql.auth.clientID must not contain whitespace or control characters")
		}
		for i, scope := range b.Spec.GraphQL.Auth.Scopes {
			if scope == "" || strings.ContainsAny(scope, " \t\r\n\x00") {
				return fmt.Errorf("binding_validation_failure: spec.graphql.auth.scopes[%d] must be non-empty and must not contain whitespace or control characters", i)
			}
		}
		return validateRequiredSecretRef(b, "spec.graphql.auth.clientSecretRef", b.Spec.GraphQL.Auth.ClientSecretRef, []string{"clientSecret"})
	default:
		return fmt.Errorf("binding_validation_failure: spec.graphql.auth.type must be bearerToken or keycloakClientCredentials")
	}
}

func validateScenarioBinding(s Scenario, b TargetBinding) error {
	return validateScenarioBindingWithProviders(s, b, nil)
}

func validateScenarioBindingWithProviders(s Scenario, b TargetBinding, providers []Provider) error {
	registry, err := NewProviderRegistryWithProviders(providers)
	if err != nil {
		return err
	}
	for name := range b.Spec.ScenarioParameters {
		if _, ok := s.Spec.Parameters[name]; !ok {
			return fmt.Errorf("binding_validation_failure: spec.scenarioParameters references unknown parameter %q", name)
		}
		if err := validateParameterValue("binding_validation_failure: spec.scenarioParameters."+name, b.Spec.ScenarioParameters[name]); err != nil {
			return err
		}
	}
	resolved, err := validateAndResolveParameters(s, b)
	if err != nil {
		return err
	}
	for _, op := range s.Spec.Operations {
		if op.Type == "mqtt.publish" || op.Type == "mqtt.roundtrip" {
			if b.Spec.MQTT.BrokerURL == "" {
				return fmt.Errorf("binding_validation_failure: spec.mqtt.brokerURL is required because operation %q uses %s", op.ID, op.Type)
			}
			if err := validateMQTTTopicParameterValues("operation "+op.ID+" mqtt.topic", op.MQTT.Topic, resolved); err != nil {
				return err
			}
		}
		if op.Type == "graphql.expect" {
			if b.Spec.GraphQL.Endpoint == "" {
				return fmt.Errorf("binding_validation_failure: spec.graphql.endpoint is required because operation %q uses graphql.expect", op.ID)
			}
			if err := validateGraphQLAuth(b); err != nil {
				return err
			}
		}
		if op.Type == "redpanda.contains" {
			if b.Spec.Redpanda.Brokers == "" {
				return fmt.Errorf("binding_validation_failure: spec.redpanda.brokers is required because operation %q uses redpanda.contains", op.ID)
			}
			topicRef := op.Redpanda.TopicRef
			topic, ok := b.Spec.Redpanda.Topics[topicRef]
			if !ok {
				return fmt.Errorf("binding_validation_failure: operation %q references unknown redpanda topicRef %q", op.ID, topicRef)
			}
			if !topic.AllowOffsetSnapshot {
				return fmt.Errorf("binding_validation_failure: operation %q redpanda topicRef %q must set allowOffsetSnapshot: true", op.ID, topicRef)
			}
			if topic.AllowCompacted {
				return fmt.Errorf("binding_validation_failure: operation %q redpanda topicRef %q uses compacted topics, unsupported in this release", op.ID, topicRef)
			}
		}
		if op.Type == "mongodb.expect" {
			if b.Spec.MongoDB.URI == "" {
				return fmt.Errorf("binding_validation_failure: spec.mongodb.uri is required because operation %q uses mongodb.expect", op.ID)
			}
			if b.Spec.MongoDB.Database == "" {
				return fmt.Errorf("binding_validation_failure: spec.mongodb.database is required because operation %q uses mongodb.expect", op.ID)
			}
			if b.Spec.MongoDB.Deployment == "atlas" && !strings.HasPrefix(b.Spec.MongoDB.URI, "mongodb+srv://") {
				return fmt.Errorf("binding_validation_failure: spec.mongodb.uri should use mongodb+srv:// because operation %q uses an Atlas deployment", op.ID)
			}
		}
		if op.Type == "postgresql.expect" {
			if b.Spec.PostgreSQL.URI == "" {
				return fmt.Errorf("binding_validation_failure: spec.postgresql.uri is required because operation %q uses postgresql.expect", op.ID)
			}
		}
		if op.Type == "rabbitmq.publish" || op.Type == "rabbitmq.expect" {
			if b.Spec.RabbitMQ.URI == "" {
				return fmt.Errorf("binding_validation_failure: spec.rabbitmq.uri is required because operation %q uses %s", op.ID, op.Type)
			}
		}
		if err := validateGenericOperationBinding(op, b, registry); err != nil {
			return err
		}
	}
	return nil
}

func validateGenericOperationBinding(op Operation, b TargetBinding, registry *ProviderRegistry) error {
	if len(op.With) == 0 {
		return nil
	}
	capability, ok := registry.ResolveCapability(op.Type)
	if !ok {
		return nil
	}
	bindings, err := ResolveGenericBindings(b)
	if err != nil {
		return err
	}
	bindingRef, _ := op.With[bindingRefKey].(string)
	if bindingRef == "" {
		bindingRef = legacyBindingName(capability.Provider)
	}
	binding, ok := bindings[bindingRef]
	if !ok {
		return fmt.Errorf("binding_validation_failure: operation %q references unknown binding %q", op.ID, bindingRef)
	}
	if binding.Kind != capability.Capability.BindingKind {
		return fmt.Errorf("binding_validation_failure: operation %q binding %q has kind %q, expected %q", op.ID, bindingRef, binding.Kind, capability.Capability.BindingKind)
	}
	if bindingSchema, ok := registry.ResolveBindingSchema(binding.Kind); ok {
		schema := bindingSchema.Schema.Schema.Schema
		if schema != nil {
			if err := ValidateJSONSchema("binding."+binding.Name+".with", *schema, binding.With); err != nil {
				return fmt.Errorf("binding_validation_failure: operation %q binding %q schema validation failed: %w", op.ID, bindingRef, err)
			}
		}
	}
	if err := validateProviderSpecificOperationBinding(op, binding); err != nil {
		return err
	}
	return nil
}

func validateProviderSpecificOperationBinding(op Operation, binding GenericBinding) error {
	switch op.Type {
	case "influxdb.expect":
		return validateInfluxDBOperationBinding(op, binding)
	default:
		return nil
	}
}

func validateInfluxDBOperationBinding(op Operation, binding GenericBinding) error {
	version, _ := binding.With["version"].(string)
	language, _ := op.With["language"].(string)
	switch version {
	case "v2":
		if language != "" && language != "flux" {
			return fmt.Errorf("binding_validation_failure: operation %q influxdb.language must be flux for InfluxDB v2", op.ID)
		}
	case "v3":
		if language == "flux" {
			return fmt.Errorf("binding_validation_failure: operation %q influxdb.language must be sql or influxql for InfluxDB v3", op.ID)
		}
	}
	return nil
}

func validateAndResolveParameters(s Scenario, b TargetBinding) (map[string]string, error) {
	resolved := map[string]string{}
	for name, parameter := range s.Spec.Parameters {
		value, ok := b.Spec.ScenarioParameters[name]
		if !ok {
			value = parameter.Default
		}
		if value == "" {
			return nil, fmt.Errorf("binding_validation_failure: required parameter %q has no binding value or scenario default", name)
		}
		resolved[name] = value
	}
	return resolved, nil
}

func validateParameterValue(field, value string) error {
	if value == "" {
		return nil
	}
	if templateRefPattern.MatchString(value) {
		return fmt.Errorf("%s must not contain template expressions", field)
	}
	return nil
}

func validateMQTTTopicParameterValues(field, topicTemplate string, params map[string]string) error {
	for _, name := range templateParamRefs(topicTemplate) {
		value := params[name]
		if strings.ContainsAny(value, "#+\x00\r\n/") {
			return fmt.Errorf("%s parameter %q value violates MQTT topic restrictions", field, name)
		}
	}
	return nil
}

func validateMQTTTopicTemplate(field, value string) error {
	if strings.ContainsAny(value, "#+\x00\r\n") {
		return fmt.Errorf("%s must not contain MQTT wildcard or control characters", field)
	}
	return nil
}

func templateParamRefs(value string) []string {
	seen := map[string]bool{}
	var refs []string
	for _, match := range templateRefPattern.FindAllStringSubmatch(value, -1) {
		ref := match[1]
		if !strings.HasPrefix(ref, "param.") {
			continue
		}
		name := strings.TrimPrefix(ref, "param.")
		if seen[name] {
			continue
		}
		seen[name] = true
		refs = append(refs, name)
	}
	return refs
}

func validateDataKey(field, value string) error {
	if value == "" || len(value) > 253 || !configMapDataKeyPattern.MatchString(value) {
		return fmt.Errorf("%s %q must be a valid ConfigMap or Secret data key: non-empty, <=253 chars, and only alphanumeric, '-', '_' or '.'", field, value)
	}
	return nil
}

func defaultSecretEnvName(secretID, logicalKey string) string {
	normalized := strings.NewReplacer("-", "_", ".", "_").Replace(secretID + "_" + logicalKey)
	return "SPEX_" + strings.ToUpper(normalized)
}

func validateSecretRef(b TargetBinding, field, ref string, keys []string) error {
	if ref == "" {
		return nil
	}
	return validateRequiredSecretRef(b, field, ref, keys)
}

func validateRequiredSecretRef(b TargetBinding, field, ref string, keys []string) error {
	if ref == "" {
		return fmt.Errorf("binding_validation_failure: %s is required", field)
	}
	secret, ok := b.Spec.Secrets[ref]
	if !ok {
		return fmt.Errorf("binding_validation_failure: %s references unknown secret %q", field, ref)
	}
	for _, key := range keys {
		if secret.Keys[key] == "" {
			return fmt.Errorf("binding_validation_failure: secret %q missing key mapping %q", ref, key)
		}
	}
	return nil
}

func validateRedpandaBrokers(field, value string) error {
	if value == "" {
		return nil
	}
	for i, raw := range strings.Split(value, ",") {
		broker := strings.TrimSpace(raw)
		if broker == "" {
			return fmt.Errorf("binding_validation_failure: %s contains empty broker at index %d", field, i)
		}
		if strings.Contains(broker, "://") {
			return fmt.Errorf("binding_validation_failure: %s broker %q must be host:port, not a URL", field, broker)
		}
		host, port, err := net.SplitHostPort(broker)
		if err != nil {
			return fmt.Errorf("binding_validation_failure: %s broker %q must be host:port", field, broker)
		}
		if host == "" || strings.ContainsAny(host, " \t\r\n") {
			return fmt.Errorf("binding_validation_failure: %s broker %q has invalid host", field, broker)
		}
		parsedPort, err := strconv.Atoi(port)
		if err != nil || parsedPort < 1 || parsedPort > 65535 {
			return fmt.Errorf("binding_validation_failure: %s broker %q has invalid port", field, broker)
		}
	}
	return nil
}

func validateKafkaTopicName(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) > 249 || value == "." || value == ".." || !kafkaTopicNamePattern.MatchString(value) {
		return fmt.Errorf("%s %q must be a valid Kafka topic name: 1-249 chars, alphanumeric, '.', '_' or '-', and not '.' or '..'", field, value)
	}
	return nil
}

func validateURLNoCredentials(field, raw string, allowedSchemes []string) error {
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s is not a valid URL: %w", field, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("binding_validation_failure: %s must include URL scheme and host", field)
	}
	allowed := false
	for _, scheme := range allowedSchemes {
		if parsed.Scheme == scheme {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("binding_validation_failure: %s uses unsupported URL scheme %q", field, parsed.Scheme)
	}
	if parsed.User != nil || strings.Contains(parsed.Host, "@") {
		return fmt.Errorf("binding_validation_failure: %s must not contain embedded credentials", field)
	}
	return nil
}
