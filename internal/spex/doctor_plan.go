package spex

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pruefwerk/spex/internal/workspace"
)

const (
	ExitValidation = 2
	ExitRuntime    = 3
	ExitPreflight  = 4
)

type ExitError struct {
	Code int
	Err  error
}

func (e ExitError) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e ExitError) Unwrap() error {
	return e.Err
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if e, ok := err.(ExitError); ok {
		return e.Code
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "kuttl failed"), strings.Contains(msg, "scenario failed"):
		return ExitRuntime
	case strings.Contains(msg, "validation"), strings.Contains(msg, "requires"), strings.Contains(msg, "unsupported"), strings.Contains(msg, "unknown"):
		return ExitValidation
	default:
		return 1
	}
}

type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type doctorOutput struct {
	Status string        `json:"status"`
	Checks []doctorCheck `json:"checks"`
}

func runDoctor(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	suitePath := fs.String("suite", "", "optional scenario suite YAML path")
	format := fs.String("format", "text", "output format: text or json")
	skipHostTools := fs.Bool("skip-host-tools", false, "skip docker/kind/kubectl/helm/go host tool checks")
	requirePinnedGitRefs := fs.Bool("require-pinned-git-refs", false, "fail when suite Git refs use mutable branch names")
	requirePinnedImages := fs.Bool("require-pinned-images", false, "fail when generated probe images are not pinned by digest")
	var artifactScanPaths multiFlag
	fs.Var(&artifactScanPaths, "scan-artifacts", "scan a generated artifact/report directory for leaked SPEX secret values and unsafe kubeconfig files; repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "doctor"); err != nil {
		return err
	}
	if *format != "text" && *format != "json" {
		return fmt.Errorf("doctor --format must be text or json")
	}
	out := doctorOutput{Status: "passed"}
	secretEnvNames := map[string]bool{}
	if !*skipHostTools {
		for _, tool := range []string{"docker", "kind", "kubectl", "helm", "go"} {
			path, err := exec.LookPath(tool)
			if err != nil {
				out.Status = "failed"
				out.Checks = append(out.Checks, doctorCheck{Name: "tool:" + tool, Status: "failed", Message: "not found on PATH"})
				continue
			}
			out.Checks = append(out.Checks, doctorCheck{Name: "tool:" + tool, Status: "passed", Message: path})
		}
	} else {
		out.Checks = append(out.Checks, doctorCheck{Name: "tool:host", Status: "skipped", Message: "skipped by --skip-host-tools"})
	}
	if *suitePath != "" {
		resolved, err := workspace.LoadScenarioSuite(*suitePath)
		if err != nil {
			out.Status = "failed"
			out.Checks = append(out.Checks, doctorCheck{Name: "suite", Status: "failed", Message: err.Error()})
		} else {
			out.Checks = append(out.Checks, doctorCheck{Name: "suite", Status: "passed", Message: resolved.Path})
			for _, warning := range mutableExternalRefWarnings(resolved.Suite) {
				status := "warning"
				if *requirePinnedGitRefs {
					status = "failed"
					out.Status = "failed"
				}
				out.Checks = append(out.Checks, doctorCheck{Name: "gitRef", Status: status, Message: warning})
			}
			inputs, err := loadSuiteInputs(resolved, suiteFlags{suitePath: *suitePath})
			if err != nil {
				out.Status = "failed"
				out.Checks = append(out.Checks, doctorCheck{Name: "suite.inputs", Status: "failed", Message: err.Error()})
			} else {
				out.Checks = append(out.Checks, doctorCheck{Name: "suite.inputs", Status: "passed", Message: fmt.Sprintf("%d scenario(s)", len(inputs))})
				for _, envName := range secretEnvironmentNamesFromInputs(inputs) {
					secretEnvNames[envName] = true
				}
				for _, check := range secretMaterializationChecks(inputs) {
					out.Checks = append(out.Checks, check)
					if check.Status == "failed" {
						out.Status = "failed"
					}
				}
				if *requirePinnedImages {
					for _, check := range pinnedImageChecks(inputs) {
						out.Checks = append(out.Checks, check)
						if check.Status == "failed" {
							out.Status = "failed"
						}
					}
					for _, check := range bundlePinnedImageChecks(resolved.Bundles) {
						out.Checks = append(out.Checks, check)
						if check.Status == "failed" {
							out.Status = "failed"
						}
					}
				}
			}
			if resolved.IntegrationProfilePath != "" {
				profile, err := workspace.LoadIntegrationProfile(resolved.IntegrationProfilePath)
				if err != nil {
					out.Status = "failed"
					out.Checks = append(out.Checks, doctorCheck{Name: "integrationProfile", Status: "failed", Message: err.Error()})
				} else {
					out.Checks = append(out.Checks, doctorCheck{Name: "integrationProfile", Status: "passed", Message: resolved.IntegrationProfilePath})
					for _, envName := range requiredEnvironmentVariables(profile) {
						if secretLikeEnvName(envName) {
							secretEnvNames[envName] = true
						}
						if os.Getenv(envName) == "" {
							out.Status = "failed"
							out.Checks = append(out.Checks, doctorCheck{Name: "env:" + envName, Status: "failed", Message: "not set"})
						} else {
							out.Checks = append(out.Checks, doctorCheck{Name: "env:" + envName, Status: "passed"})
						}
					}
				}
			}
		}
	}
	for _, envName := range ambientSecretEnvironmentNames() {
		secretEnvNames[envName] = true
	}
	if len(artifactScanPaths) > 0 {
		for _, check := range artifactSecretScanChecks(artifactScanPaths, sortedEnvNames(secretEnvNames)) {
			out.Checks = append(out.Checks, check)
			if check.Status == "failed" {
				out.Status = "failed"
			}
		}
	}
	if *format == "json" {
		content, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(content))
	} else {
		fmt.Fprintf(stdout, "doctor: %s\n", out.Status)
		for _, check := range out.Checks {
			if check.Message == "" {
				fmt.Fprintf(stdout, "- %s: %s\n", check.Name, check.Status)
			} else {
				fmt.Fprintf(stdout, "- %s: %s (%s)\n", check.Name, check.Status, check.Message)
			}
		}
	}
	if out.Status != "passed" {
		return ExitError{Code: ExitPreflight, Err: fmt.Errorf("doctor preflight failed")}
	}
	return nil
}

func pinnedImageChecks(inputs []workspace.Inputs) []doctorCheck {
	var checks []doctorCheck
	for _, input := range inputs {
		image := strings.TrimSpace(input.Binding.Spec.Probe.Image)
		name := "imageRef:" + input.ScenarioName + ":probe"
		if image == "" {
			checks = append(checks, doctorCheck{Name: name, Status: "failed", Message: "probe image is empty"})
			continue
		}
		if !pinnedImageDigestPattern.MatchString(image) {
			checks = append(checks, doctorCheck{Name: name, Status: "failed", Message: fmt.Sprintf("%s is not pinned by a valid sha256 digest", image)})
			continue
		}
		checks = append(checks, doctorCheck{Name: name, Status: "passed", Message: image})
	}
	return checks
}

func bundlePinnedImageChecks(bundles []workspace.ResolvedBundle) []doctorCheck {
	var checks []doctorCheck
	for _, bundle := range bundles {
		if bundle.SourceType == "builtin" {
			continue
		}
		for _, capability := range bundle.Provider.Capabilities {
			image := strings.TrimSpace(capability.Probe.Image)
			name := "bundleImageRef:" + bundle.Name + ":" + capability.Type
			if image == "" {
				checks = append(checks, doctorCheck{Name: name, Status: "failed", Message: "bundle probe image is empty"})
				continue
			}
			if !pinnedImageDigestPattern.MatchString(image) {
				checks = append(checks, doctorCheck{Name: name, Status: "failed", Message: fmt.Sprintf("%s is not pinned by a valid sha256 digest", image)})
				continue
			}
			checks = append(checks, doctorCheck{Name: name, Status: "passed", Message: image})
		}
	}
	return checks
}

func secretMaterializationChecks(inputs []workspace.Inputs) []doctorCheck {
	checkedEnvFiles := map[string]envFileCheck{}
	needsAWS := false
	var checks []doctorCheck
	for _, input := range inputs {
		for id, secret := range input.Binding.Spec.Secrets {
			switch secret.Type {
			case "localEnvFile":
				envFile := secret.EnvFile
				if !filepath.IsAbs(envFile) && input.BindingPath != "" {
					envFile = filepath.Join(filepath.Dir(input.BindingPath), envFile)
				}
				envCheck, ok := checkedEnvFiles[envFile]
				if !ok {
					envCheck = readEnvFileForDoctor(envFile)
					checkedEnvFiles[envFile] = envCheck
				}
				name := "secret:" + id + ":localEnvFile"
				if envCheck.err != nil {
					checks = append(checks, doctorCheck{Name: name, Status: "failed", Message: fmt.Sprintf("%s: %v", envFile, envCheck.err)})
				} else {
					checks = append(checks, doctorCheck{Name: name, Status: "passed", Message: envFile})
					for _, envName := range missingLocalEnvNames(id, secret, envCheck.names) {
						checks = append(checks, doctorCheck{Name: "secret:" + id + ":env:" + envName, Status: "warning", Message: fmt.Sprintf("%s does not appear to define %s", envFile, envName)})
					}
				}
			case "awsSsmParameter":
				needsAWS = true
				for logicalKey, value := range secret.SSMParameters {
					if strings.TrimSpace(ssmParameterNameForDoctor(value)) == "" {
						checks = append(checks, doctorCheck{Name: "secret:" + id + ":awsSsmParameter", Status: "failed", Message: fmt.Sprintf("empty SSM parameter for key %s", logicalKey)})
					}
				}
			}
		}
	}
	if needsAWS {
		if path, err := exec.LookPath("aws"); err != nil {
			checks = append(checks, doctorCheck{Name: "tool:aws", Status: "failed", Message: "required by awsSsmParameter secrets and not found on PATH"})
		} else {
			checks = append(checks, doctorCheck{Name: "tool:aws", Status: "passed", Message: path})
		}
	}
	return checks
}

type envFileCheck struct {
	names map[string]bool
	err   error
}

var envFileAssignmentPattern = regexp.MustCompile(`(?m)^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=`)

func readEnvFileForDoctor(path string) envFileCheck {
	content, err := readRegularEvidenceFile(path, 1<<20)
	if err != nil {
		return envFileCheck{err: err}
	}
	names := map[string]bool{}
	for _, match := range envFileAssignmentPattern.FindAllSubmatch(content, -1) {
		names[string(match[1])] = true
	}
	return envFileCheck{names: names}
}

func missingLocalEnvNames(secretID string, secret workspace.Secret, names map[string]bool) []string {
	var missing []string
	for logicalKey := range secret.Keys {
		envName := secret.Env[logicalKey]
		if envName == "" {
			envName = defaultSecretEnvNameForDoctor(secretID, logicalKey)
		}
		if !names[envName] {
			missing = append(missing, envName)
		}
	}
	sort.Strings(missing)
	return missing
}

func defaultSecretEnvNameForDoctor(secretID, logicalKey string) string {
	normalized := strings.NewReplacer("-", "_", ".", "_").Replace(secretID + "_" + logicalKey)
	return "SPEX_" + strings.ToUpper(normalized)
}

func secretEnvironmentNamesFromInputs(inputs []workspace.Inputs) []string {
	seen := map[string]bool{}
	for _, input := range inputs {
		for id, secret := range input.Binding.Spec.Secrets {
			if secret.Type != "localEnvFile" {
				continue
			}
			for logicalKey := range secret.Keys {
				envName := secret.Env[logicalKey]
				if envName == "" {
					envName = defaultSecretEnvNameForDoctor(id, logicalKey)
				}
				seen[envName] = true
			}
		}
	}
	return sortedEnvNames(seen)
}

func ambientSecretEnvironmentNames() []string {
	seen := map[string]bool{}
	for _, entry := range os.Environ() {
		envName, _, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(envName, "SPEX_") && secretLikeEnvName(envName) {
			seen[envName] = true
		}
	}
	return sortedEnvNames(seen)
}

func secretLikeEnvName(envName string) bool {
	name := strings.ToUpper(envName)
	for _, token := range []string{"PASSWORD", "TOKEN", "SECRET", "API_KEY", "CLIENT_SECRET"} {
		if strings.Contains(name, token) {
			return true
		}
	}
	return false
}

func sortedEnvNames(names map[string]bool) []string {
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func artifactSecretScanChecks(paths []string, envNames []string) []doctorCheck {
	values := map[string]string{}
	for _, envName := range envNames {
		value := os.Getenv(envName)
		if len(value) >= 8 {
			values[envName] = value
		}
	}
	var checks []doctorCheck
	for _, root := range paths {
		root = strings.TrimSpace(root)
		if root == "" {
			checks = append(checks, doctorCheck{Name: "artifactSecretScan", Status: "failed", Message: "empty artifact scan path"})
			continue
		}
		info, err := os.Lstat(root)
		if err != nil {
			checks = append(checks, doctorCheck{Name: "artifactSecretScan:" + root, Status: "failed", Message: err.Error()})
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			checks = append(checks, doctorCheck{Name: "artifactSecretScan:" + root, Status: "failed", Message: "scan root is a symlink"})
			continue
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			checks = append(checks, doctorCheck{Name: "artifactSecretScan:" + root, Status: "failed", Message: "scan root is not a regular file or directory"})
			continue
		}
		kubeconfigs := artifactKubeconfigFiles(root)
		if len(kubeconfigs) > 0 {
			for _, path := range kubeconfigs {
				checks = append(checks, doctorCheck{Name: "artifactKubeconfigScan", Status: "failed", Message: fmt.Sprintf("kubeconfig file found in %s", path)})
			}
			continue
		}
		if len(values) == 0 {
			checks = append(checks, doctorCheck{Name: "artifactSecretScan:" + root, Status: "passed", Message: "no non-empty secret environment values selected for scanning; no kubeconfig files found"})
			continue
		}
		leaks := artifactSecretLeaks(root, values)
		if len(leaks) == 0 {
			checks = append(checks, doctorCheck{Name: "artifactSecretScan:" + root, Status: "passed", Message: fmt.Sprintf("scanned for %d secret environment value(s)", len(values))})
			continue
		}
		for _, leak := range leaks {
			checks = append(checks, doctorCheck{Name: "artifactSecretScan:" + leak.envName, Status: "failed", Message: fmt.Sprintf("secret value found in %s", leak.path)})
		}
	}
	return checks
}

func artifactKubeconfigFiles(root string) []string {
	var matches []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".spex":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		name := entry.Name()
		if name == "kubeconfig" || strings.HasSuffix(name, ".kubeconfig") {
			matches = append(matches, path)
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 10*1024*1024 {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if looksLikeKubeconfig(content) {
			matches = append(matches, path)
		}
		return nil
	})
	sort.Strings(matches)
	return matches
}

func looksLikeKubeconfig(content []byte) bool {
	text := string(content)
	return strings.Contains(text, "apiVersion:") &&
		strings.Contains(text, "clusters:") &&
		strings.Contains(text, "contexts:") &&
		strings.Contains(text, "users:")
}

type artifactSecretLeak struct {
	envName string
	path    string
}

func artifactSecretLeaks(root string, values map[string]string) []artifactSecretLeak {
	var leaks []artifactSecretLeak
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			leaks = append(leaks, artifactSecretLeak{envName: "walk", path: fmt.Sprintf("%s: %v", path, err)})
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".spex":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		info, statErr := entry.Info()
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 10*1024*1024 {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		text := string(content)
		for envName, value := range values {
			if strings.Contains(text, value) {
				leaks = append(leaks, artifactSecretLeak{envName: envName, path: path})
			}
		}
		return nil
	})
	sort.Slice(leaks, func(i, j int) bool {
		if leaks[i].envName == leaks[j].envName {
			return leaks[i].path < leaks[j].path
		}
		return leaks[i].envName < leaks[j].envName
	})
	return leaks
}

var doctorSSMReferencePattern = regexp.MustCompile(`^\s*\{\{\s*ssm\s+"([^"]+)"\s*\}\}\s*$`)
var pinnedImageDigestPattern = regexp.MustCompile(`@sha256:[a-f0-9]{64}$`)

func ssmParameterNameForDoctor(value string) string {
	if match := doctorSSMReferencePattern.FindStringSubmatch(value); match != nil {
		return match[1]
	}
	return strings.TrimSpace(value)
}

func mutableExternalRefWarnings(suite workspace.ScenarioSuite) []string {
	var refs []string
	refs = append(refs, suite.Spec.BindingRef, suite.Spec.IntegrationProfileRef)
	refs = append(refs, suite.Spec.CatalogRefs...)
	for _, scenario := range suite.Spec.Scenarios {
		refs = append(refs, scenario.BindingRef, scenario.IntegrationProfileRef)
	}
	var warnings []string
	for _, ref := range refs {
		if ref == "" || !strings.Contains(ref, "@") {
			continue
		}
		version := ref[strings.LastIndex(ref, "@")+1:]
		switch version {
		case "main", "master", "develop", "dev":
			warnings = append(warnings, fmt.Sprintf("%s uses mutable ref @%s; pin to a tag or commit SHA for CI repeatability", ref, version))
		}
	}
	return warnings
}

var envRefPattern = regexp.MustCompile(`\$[{]?([A-Z_][A-Z0-9_]*)[}]?`)

func requiredEnvironmentVariables(profile workspace.IntegrationProfile) []string {
	seen := map[string]bool{}
	for _, command := range append(profile.Spec.KIND.Commands, profile.Spec.Setup.Commands...) {
		for _, match := range envRefPattern.FindAllStringSubmatch(command.Command, -1) {
			if strings.HasPrefix(match[1], "SPEX_") {
				seen[match[1]] = true
			}
		}
	}
	var out []string
	for envName := range seen {
		out = append(out, envName)
	}
	sort.Strings(out)
	return out
}

type suitePlanOutput struct {
	Suite                  string               `json:"suite"`
	SuiteFile              string               `json:"suiteFile"`
	BindingFile            string               `json:"bindingFile"`
	IntegrationProfileFile string               `json:"integrationProfileFile,omitempty"`
	CatalogFiles           []string             `json:"catalogFiles,omitempty"`
	Providers              []suiteProvider      `json:"providers,omitempty"`
	WorkspaceRoot          string               `json:"workspaceRoot"`
	ReportDir              string               `json:"reportDir"`
	Scenarios              []suitePlanScenario  `json:"scenarios"`
	HelmApps               []suitePlanHelmApp   `json:"helmApps,omitempty"`
	RequiredSecrets        []suitePlanSecretRef `json:"requiredSecrets,omitempty"`
	RequiredEnv            []string             `json:"requiredEnv,omitempty"`
}

type suitePlanScenario struct {
	Name         string          `json:"name"`
	File         string          `json:"file"`
	Namespace    string          `json:"namespace"`
	Operations   []string        `json:"operations"`
	Capabilities []suiteProvider `json:"capabilities,omitempty"`
}

type suiteProvider struct {
	Provider      string   `json:"provider"`
	OperationType string   `json:"operationType,omitempty"`
	BindingKind   string   `json:"bindingKind,omitempty"`
	BindingNames  []string `json:"bindingNames,omitempty"`
}

type suitePlanHelmApp struct {
	Name      string   `json:"name"`
	Chart     string   `json:"chart"`
	Namespace string   `json:"namespace,omitempty"`
	Values    []string `json:"values,omitempty"`
}

type suitePlanSecretRef struct {
	ID   string   `json:"id"`
	Type string   `json:"type"`
	Name string   `json:"name,omitempty"`
	Keys []string `json:"keys"`
}

func runSuitePlan(args []string, stdout io.Writer) error {
	flags, format, err := parseSuitePlanFlags(args)
	if err != nil {
		return err
	}
	resolved, err := workspace.LoadScenarioSuite(flags.suitePath)
	if err != nil {
		return fmt.Errorf("suite: %w", err)
	}
	inputs, err := loadSuiteInputs(resolved, flags)
	if err != nil {
		return err
	}
	outRoot := suiteOutputRoot(resolved, flags)
	plan := suitePlanOutput{
		Suite:                  resolved.Suite.Metadata.Name,
		SuiteFile:              resolved.Path,
		BindingFile:            resolved.BindingPath,
		IntegrationProfileFile: resolved.IntegrationProfilePath,
		CatalogFiles:           resolved.CatalogPaths,
		WorkspaceRoot:          outRoot,
		ReportDir:              suiteReportDir(resolved, flags, outRoot),
	}
	if len(inputs) > 0 {
		plan.RequiredSecrets = planSecretRefs(inputs[0].Binding.Spec.Secrets)
	}
	providerSet := map[string]suiteProvider{}
	seenHelmApps := map[string]bool{}
	seenEnv := map[string]bool{}
	for _, input := range inputs {
		scenario := suitePlanScenario{Name: input.ScenarioName, File: input.ScenarioPath, Namespace: input.Namespace}
		for _, op := range input.Scenario.Spec.Operations {
			scenario.Operations = append(scenario.Operations, op.ID+":"+op.Type)
		}
		for _, provider := range suiteProvidersForInput(input) {
			scenario.Capabilities = append(scenario.Capabilities, provider)
			providerSet[provider.Provider+"\x00"+provider.OperationType+"\x00"+provider.BindingKind] = provider
		}
		plan.Scenarios = append(plan.Scenarios, scenario)
		if input.Integration != nil {
			for _, app := range input.Integration.Spec.HelmApps {
				namespace := app.Namespace
				if namespace == "" {
					namespace = input.Namespace
				}
				helmKey := app.Name + "\x00" + app.Chart + "\x00" + namespace
				if !seenHelmApps[helmKey] {
					seenHelmApps[helmKey] = true
					plan.HelmApps = append(plan.HelmApps, suitePlanHelmApp{Name: app.Name, Chart: app.Chart, Namespace: namespace, Values: app.Values})
				}
			}
			for _, envName := range requiredEnvironmentVariables(*input.Integration) {
				seenEnv[envName] = true
			}
		}
	}
	for envName := range seenEnv {
		plan.RequiredEnv = append(plan.RequiredEnv, envName)
	}
	sort.Strings(plan.RequiredEnv)
	plan.Providers = sortedSuiteProviders(providerSet)
	if format == "json" {
		content, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(content))
		return nil
	}
	fmt.Fprintf(stdout, "suite: %s\n", plan.Suite)
	fmt.Fprintf(stdout, "binding: %s\n", plan.BindingFile)
	if plan.IntegrationProfileFile != "" {
		fmt.Fprintf(stdout, "integrationProfile: %s\n", plan.IntegrationProfileFile)
	}
	fmt.Fprintf(stdout, "workspaceRoot: %s\n", plan.WorkspaceRoot)
	fmt.Fprintf(stdout, "reportDir: %s\n", plan.ReportDir)
	if len(plan.HelmApps) > 0 {
		fmt.Fprintln(stdout, "helmApps:")
		for _, app := range plan.HelmApps {
			fmt.Fprintf(stdout, "  - %s: %s -> %s\n", app.Name, app.Chart, app.Namespace)
		}
	}
	if len(plan.RequiredSecrets) > 0 {
		fmt.Fprintln(stdout, "requiredSecrets:")
		for _, secret := range plan.RequiredSecrets {
			fmt.Fprintf(stdout, "  - %s: %s %s keys=%s\n", secret.ID, secret.Type, secret.Name, strings.Join(secret.Keys, ","))
		}
	}
	if len(plan.RequiredEnv) > 0 {
		fmt.Fprintf(stdout, "requiredEnv: %s\n", strings.Join(plan.RequiredEnv, ","))
	}
	if len(plan.Providers) > 0 {
		fmt.Fprintln(stdout, "providers:")
		for _, provider := range plan.Providers {
			fmt.Fprintf(stdout, "  - %s: %s bindingKind=%s bindings=%s\n", provider.Provider, provider.OperationType, provider.BindingKind, strings.Join(provider.BindingNames, ","))
		}
	}
	fmt.Fprintln(stdout, "scenarios:")
	for _, scenario := range plan.Scenarios {
		fmt.Fprintf(stdout, "  - %s (%s) namespace=%s operations=%d\n", scenario.Name, scenario.File, scenario.Namespace, len(scenario.Operations))
	}
	return nil
}

func parseSuitePlanFlags(args []string) (suiteFlags, string, error) {
	flags, err := parseSuiteFlags("plan", args)
	if err != nil {
		return flags, "", err
	}
	format := flags.format
	if format != "text" && format != "json" {
		return flags, "", fmt.Errorf("suite plan --format must be text or json")
	}
	return flags, format, nil
}

func suiteProvidersForInput(input workspace.Inputs) []suiteProvider {
	registry, err := workspace.NewProviderRegistryWithProviders(input.Providers)
	if err != nil {
		return nil
	}
	lowered, err := workspace.LowerOperations(input, registry)
	if err != nil {
		return nil
	}
	out := make([]suiteProvider, 0, len(lowered))
	for _, operation := range lowered {
		out = append(out, suiteProvider{
			Provider:      operation.Provider,
			OperationType: operation.OperationType,
			BindingKind:   operation.Binding.Kind,
			BindingNames:  []string{operation.Binding.Name},
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].OperationType < out[j].OperationType
	})
	return out
}

func sortedSuiteProviders(providers map[string]suiteProvider) []suiteProvider {
	out := make([]suiteProvider, 0, len(providers))
	for _, provider := range providers {
		out = append(out, provider)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].OperationType < out[j].OperationType
	})
	return out
}

func planSecretRefs(secrets map[string]workspace.Secret) []suitePlanSecretRef {
	var ids []string
	for id := range secrets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]suitePlanSecretRef, 0, len(ids))
	for _, id := range ids {
		secret := secrets[id]
		var keys []string
		for key := range secret.Keys {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out = append(out, suitePlanSecretRef{ID: id, Type: secret.Type, Name: secret.Name, Keys: keys})
	}
	return out
}

func runCatalogCheck(args []string, stdout io.Writer) error {
	bundle, format, err := loadCatalogsForCommand("check", args)
	if err != nil {
		return err
	}
	var failures []string
	seenFlows := map[string]string{}
	for _, flow := range bundle.Inventory.Flows {
		if previous := seenFlows[flow.Name]; previous != "" {
			failures = append(failures, fmt.Sprintf("duplicate flow %q in %s and %s", flow.Name, previous, flow.Source))
		}
		seenFlows[flow.Name] = flow.Source
	}
	seenSteps := map[string]string{}
	var steps []workspace.CatalogStep
	for _, step := range bundle.Inventory.Steps {
		key := step.Step.Kind + "\x00" + step.Step.Expression
		if previous := seenSteps[key]; previous != "" {
			failures = append(failures, fmt.Sprintf("duplicate step %s %q in %s and %s", step.Step.Kind, step.Step.Expression, previous, step.Source))
		}
		seenSteps[key] = step.Source
		for _, previous := range steps {
			if previous.Step.Kind != step.Step.Kind || previous.Step.Expression == step.Step.Expression {
				continue
			}
			if catalogExpressionsCanOverlap(previous.Step.Expression, step.Step.Expression) {
				failures = append(failures, fmt.Sprintf("ambiguous step expressions for %s: %q in %s overlaps with %q in %s", step.Step.Kind, step.Step.Expression, step.Source, previous.Step.Expression, previous.Source))
			}
		}
		steps = append(steps, step)
	}
	result := catalogCheckOutput{
		Status:   "passed",
		Flows:    len(bundle.Inventory.Flows),
		Steps:    len(bundle.Inventory.Steps),
		Failures: failures,
	}
	if len(failures) > 0 {
		result.Status = "failed"
		if format == "json" {
			if err := writeCatalogCheckJSON(stdout, result); err != nil {
				return err
			}
			return ExitError{Code: ExitValidation, Err: fmt.Errorf("catalog check failed")}
		}
		for _, failure := range failures {
			fmt.Fprintf(stdout, "failed: %s\n", failure)
		}
		return ExitError{Code: ExitValidation, Err: fmt.Errorf("catalog check failed")}
	}
	if format == "json" {
		return writeCatalogCheckJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "catalog check passed: %d flow(s), %d step(s)\n", len(bundle.Inventory.Flows), len(bundle.Inventory.Steps))
	return nil
}

type catalogCheckOutput struct {
	Status   string   `json:"status"`
	Flows    int      `json:"flows"`
	Steps    int      `json:"steps"`
	Failures []string `json:"failures,omitempty"`
}

func writeCatalogCheckJSON(stdout io.Writer, result catalogCheckOutput) error {
	content, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(content))
	return nil
}

var catalogCheckVariablePattern = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)(?::([A-Za-z]+))?\}`)

func catalogExpressionsCanOverlap(a, b string) bool {
	return catalogExpressionMatches(b, catalogExpressionSample(a)) || catalogExpressionMatches(a, catalogExpressionSample(b))
}

func catalogExpressionSample(expression string) string {
	out := expression
	for _, match := range catalogCheckVariablePattern.FindAllStringSubmatch(expression, -1) {
		replacement := "value"
		if match[2] == "number" {
			replacement = "42"
		}
		out = strings.Replace(out, match[0], replacement, 1)
	}
	return out
}

func catalogExpressionMatches(expression, text string) bool {
	pattern := regexp.QuoteMeta(expression)
	for _, match := range catalogCheckVariablePattern.FindAllStringSubmatch(expression, -1) {
		token := regexp.QuoteMeta(match[0])
		replacement := `(.+)`
		if match[2] == "number" {
			replacement = `([+-]?(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+))`
		}
		pattern = strings.Replace(pattern, token, replacement, 1)
	}
	re, err := regexp.Compile("^" + pattern + "$")
	if err != nil {
		return false
	}
	return re.MatchString(text)
}

func runCatalogDocs(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("catalog docs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	suitePath := fs.String("suite", "", "scenario suite YAML path")
	outPath := fs.String("out", "", "Markdown output path; stdout when omitted")
	var catalogs multiFlag
	fs.Var(&catalogs, "catalog", "catalog YAML path, repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectPositionalArgs(fs, "catalog docs"); err != nil {
		return err
	}
	var loadArgs []string
	if *suitePath != "" {
		loadArgs = append(loadArgs, "--suite", *suitePath)
	}
	for _, catalog := range catalogs {
		loadArgs = append(loadArgs, "--catalog", catalog)
	}
	bundle, _, err := loadCatalogsForCommand("docs", loadArgs)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# spex Catalog\n\n")
	b.WriteString("## Flows\n\n")
	for _, flow := range bundle.Inventory.Flows {
		fmt.Fprintf(&b, "### %s\n\nSource: `%s`\n\n", flow.Name, flow.Source)
		writeExpansionCountsMarkdown(&b, flow.Flow.ExpandsTo)
		if len(flow.Flow.Parameters) > 0 {
			b.WriteString("Parameters:\n\n")
			var keys []string
			for key := range flow.Flow.Parameters {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				fmt.Fprintf(&b, "- `%s`: `%s`\n", key, flow.Flow.Parameters[key])
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("## Steps\n\n")
	for _, step := range bundle.Inventory.Steps {
		fmt.Fprintf(&b, "### %s `%s`\n\nSource: `%s`\n\n", step.Step.Kind, step.Step.Expression, step.Source)
		writeExpansionCountsMarkdown(&b, step.Step.Output)
	}
	if *outPath == "" {
		fmt.Fprint(stdout, b.String())
		return nil
	}
	if err := ensureSafeDirectory(filepath.Dir(*outPath), 0o755); err != nil {
		return err
	}
	if err := writeReportFile(*outPath, []byte(b.String())); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "catalog docs written: %s\n", *outPath)
	return nil
}

func writeExpansionCountsMarkdown(b *strings.Builder, expansion workspace.CatalogExpansion) {
	b.WriteString("Expansion:\n\n")
	fmt.Fprintf(b, "- Parameters: %d\n", len(expansion.Parameters))
	fmt.Fprintf(b, "- Payload templates: %d\n", len(expansion.PayloadTemplates))
	fmt.Fprintf(b, "- GraphQL queries: %d\n", len(expansion.GraphQLQueries))
	fmt.Fprintf(b, "- Operations: %d\n\n", len(expansion.Operations))
}
