package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type generationPlan struct {
	ScenarioSlug string
	Params       map[string]string
	Payloads     map[string]string
	Queries      map[string]string
	Variables    map[string]string
	Matchers     map[string]string
	Steps        []generatedStep
}

type generatedStep struct {
	Ordinal     int
	OperationID string
	Type        string
	ApplyFile   string
	AssertFile  string
	Job         string
	Assert      string
}

func Generate(out string, in Inputs) error {
	if err := ValidateLabelValue("runId", in.RunID); err != nil {
		return err
	}
	workspaceDir := "."
	testStepWorkspaceDir := "../.."
	repoRoot, err := os.Getwd()
	if err != nil {
		return err
	}
	if in.RepoRoot != "" {
		repoRoot = in.RepoRoot
	}
	integrationProfileDir := repoRoot
	if in.IntegrationProfilePath != "" {
		integrationProfileDir = filepath.Dir(in.IntegrationProfilePath)
	}
	plan := buildPlan(in)
	dirs := []string{
		filepath.Join(out, "kuttl", plan.ScenarioSlug),
		filepath.Join(out, "rendered", "payloads"),
		filepath.Join(out, "rendered", "queries"),
		filepath.Join(out, "rendered", "variables"),
		filepath.Join(out, "rendered", "matchers"),
		filepath.Join(out, "reports"),
		filepath.Join(out, "evidence", "logs"),
		filepath.Join(out, "evidence", "results"),
	}
	for _, dir := range dirs {
		if err := ensureSafeGeneratedDirUnderRoot(out, dir, 0o755); err != nil {
			return err
		}
	}

	files := map[string]string{
		"README.generated.md": generatedReadme(in),
		"kuttl-test.yaml":     kuttlTest(in, integrationRenderContext{WorkspaceDir: workspaceDir, RepoRoot: repoRoot, IntegrationProfileDir: integrationProfileDir}),
		"execution-plan.yaml": executionPlan(plan),
		"step-map.yaml":       stepMap(in, plan),
		filepath.Join("kuttl", plan.ScenarioSlug, "00-rerun-cleanup.yaml"):                                           cleanupStep(in, plan.ScenarioSlug, integrationRenderContext{WorkspaceDir: testStepWorkspaceDir, RepoRoot: repoRoot, IntegrationProfileDir: integrationProfileDir}),
		filepath.Join("kuttl", plan.ScenarioSlug, fmt.Sprintf("%02d-static-configmaps.yaml", staticStepOrdinal(in))): staticConfigMaps(in, plan),
	}
	if integrationSetupEnabled(in) {
		files[filepath.Join("kuttl", plan.ScenarioSlug, "01-integration-setup.yaml")] = integrationSetupStep(in, integrationRenderContext{WorkspaceDir: testStepWorkspaceDir, RepoRoot: repoRoot, IntegrationProfileDir: integrationProfileDir})
	}
	if in.Integration != nil && in.Integration.Spec.KIND.Config != "" {
		content, err := readRegularInputFile(in.Integration.Spec.KIND.Config, maxYAMLInputFileSize)
		if err != nil {
			return err
		}
		files["kind.yaml"] = string(content)
	}
	if in.Binding.Spec.RBAC.Create {
		files[filepath.Join("kuttl", plan.ScenarioSlug, fmt.Sprintf("%02d-rbac.yaml", staticStepOrdinal(in)))] = rbac(in, plan.ScenarioSlug)
	}
	for _, step := range plan.Steps {
		files[filepath.Join("kuttl", plan.ScenarioSlug, step.ApplyFile)] = step.Job
		files[filepath.Join("kuttl", plan.ScenarioSlug, step.AssertFile)] = step.Assert
	}
	for name, content := range plan.Payloads {
		files[filepath.Join("rendered", "payloads", name)] = content
	}
	for name, content := range plan.Queries {
		files[filepath.Join("rendered", "queries", name)] = content
	}
	for name, content := range plan.Variables {
		files[filepath.Join("rendered", "variables", name)] = content
	}
	for name, content := range plan.Matchers {
		files[filepath.Join("rendered", "matchers", name)] = content
	}
	for rel, content := range files {
		path, err := generatedFilePath(out, rel)
		if err != nil {
			return err
		}
		if err := writeGeneratedFile(out, path, []byte(content)); err != nil {
			return err
		}
	}
	return nil
}

func generatedFilePath(out, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("generated path %q must be relative", rel)
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("generated path %q escapes workspace", rel)
	}
	return filepath.Join(out, clean), nil
}

func ensureSafeGeneratedDir(path string, mode os.FileMode) error {
	clean := filepath.Clean(path)
	if err := rejectUnsafeGeneratedDir(clean); err != nil {
		return err
	}
	if err := os.MkdirAll(clean, mode); err != nil {
		return err
	}
	return rejectUnsafeGeneratedDir(clean)
}

func ensureSafeGeneratedDirUnderRoot(root, path string, mode os.FileMode) error {
	cleanRoot := filepath.Clean(root)
	cleanPath := filepath.Clean(path)
	if err := rejectUnsafeGeneratedPathUnderRoot(cleanRoot, cleanPath); err != nil {
		return err
	}
	if err := os.MkdirAll(cleanPath, mode); err != nil {
		return err
	}
	if err := rejectUnsafeGeneratedPathUnderRoot(cleanRoot, cleanPath); err != nil {
		return err
	}
	return rejectUnsafeGeneratedDir(cleanPath)
}

func rejectUnsafeGeneratedPathUnderRoot(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%s: generated path escapes workspace", path)
	}
	if err := rejectUnsafeGeneratedDir(root); err != nil {
		return err
	}
	current := root
	if rel == "." {
		return nil
	}
	for _, elem := range strings.Split(rel, string(filepath.Separator)) {
		if elem == "" || elem == "." {
			continue
		}
		current = filepath.Join(current, elem)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: refusing symlink directory", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s: not a directory", current)
		}
	}
	return nil
}

func rejectUnsafeGeneratedDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s: refusing symlink directory", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: not a directory", path)
	}
	return nil
}

func writeGeneratedFile(root, path string, content []byte) error {
	if err := ensureSafeGeneratedDirUnderRoot(root, filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: not a regular file", filepath.Base(path))
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if err := syncGeneratedDir(dir); err != nil {
		return err
	}
	keepTemp = true
	return nil
}

func syncGeneratedDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil && !os.IsPermission(err) && err != syscall.EINVAL {
		return err
	}
	return nil
}

func buildPlan(in Inputs) generationPlan {
	scenarioSlug := DNSLabel(in.ScenarioName)
	plan := generationPlan{
		ScenarioSlug: scenarioSlug,
		Params:       resolvedParameters(in),
		Payloads:     map[string]string{},
		Queries:      map[string]string{},
		Variables:    map[string]string{},
		Matchers:     map[string]string{},
	}

	ordinal := firstProbeOrdinal(in)
	if needsRedpandaSnapshot(in.Scenario.Spec.Operations) {
		step := generatedStep{
			Ordinal:     ordinal,
			OperationID: "redpanda-snapshot-offsets",
			Type:        "redpanda.snapshotOffsets",
			ApplyFile:   stepFile(ordinal, "redpanda-snapshot-offsets"),
			AssertFile:  assertFile(ordinal, "redpanda-snapshot-offsets"),
			Job:         redpandaSnapshotJob(in, scenarioSlug, ordinal),
			Assert:      probeJobAssert(in, scenarioSlug, ordinal, "redpanda-snapshot-offsets"),
		}
		plan.Steps = append(plan.Steps, step)
		ordinal++
	}

	for _, op := range in.Scenario.Spec.Operations {
		switch op.Type {
		case "mqtt.publish":
			payloadFile := DNSLabel(op.ID) + ".json"
			template := in.Scenario.Spec.PayloadTemplates[op.MQTT.PayloadTemplateRef]
			plan.Payloads[payloadFile] = renderJSONTemplate(template.Body, in.RunID, op.MQTT.CorrelationID, plan.Params)
			step := generatedStep{
				Ordinal:     ordinal,
				OperationID: op.ID,
				Type:        op.Type,
				ApplyFile:   stepFile(ordinal, op.ID),
				AssertFile:  assertFile(ordinal, op.ID),
				Job:         mqttPublishJob(in, scenarioSlug, ordinal, op, payloadFile, plan.Params),
				Assert:      probeJobAssert(in, scenarioSlug, ordinal, op.ID),
			}
			plan.Steps = append(plan.Steps, step)
			ordinal++
		case "rabbitmq.publish":
			payloadFile := DNSLabel(op.ID) + ".json"
			template := in.Scenario.Spec.PayloadTemplates[op.RabbitMQ.PayloadTemplateRef]
			plan.Payloads[payloadFile] = renderJSONTemplate(template.Body, in.RunID, op.RabbitMQ.CorrelationID, plan.Params)
			step := generatedStep{
				Ordinal:     ordinal,
				OperationID: op.ID,
				Type:        op.Type,
				ApplyFile:   stepFile(ordinal, op.ID),
				AssertFile:  assertFile(ordinal, op.ID),
				Job:         rabbitmqPublishJob(in, scenarioSlug, ordinal, op, payloadFile, plan.Params),
				Assert:      probeJobAssert(in, scenarioSlug, ordinal, op.ID),
			}
			plan.Steps = append(plan.Steps, step)
			ordinal++
		case "rabbitmq.expect":
			matchersFile := DNSLabel(op.ID) + ".matchers.json"
			plan.Matchers[matchersFile] = renderMatchersJSON(op.RabbitMQ.Match, in.RunID, op.RabbitMQ.CorrelationID, plan.Params)
			step := generatedStep{
				Ordinal:     ordinal,
				OperationID: op.ID,
				Type:        op.Type,
				ApplyFile:   stepFile(ordinal, op.ID),
				AssertFile:  assertFile(ordinal, op.ID),
				Job:         rabbitmqExpectJob(in, scenarioSlug, ordinal, op, matchersFile, plan.Params),
				Assert:      probeJobAssert(in, scenarioSlug, ordinal, op.ID),
			}
			plan.Steps = append(plan.Steps, step)
			ordinal++
		case "redpanda.contains":
			matchersFile := DNSLabel(op.ID) + ".matchers.json"
			plan.Matchers[matchersFile] = renderMatchersJSON(op.Redpanda.Match, in.RunID, op.Redpanda.CorrelationID, plan.Params)
			step := generatedStep{
				Ordinal:     ordinal,
				OperationID: op.ID,
				Type:        op.Type,
				ApplyFile:   stepFile(ordinal, op.ID),
				AssertFile:  assertFile(ordinal, op.ID),
				Job:         redpandaContainsJob(in, scenarioSlug, ordinal, op, matchersFile),
				Assert:      probeJobAssert(in, scenarioSlug, ordinal, op.ID),
			}
			plan.Steps = append(plan.Steps, step)
			ordinal++
		case "graphql.expect":
			query := in.Scenario.Spec.GraphQLQueries[op.GraphQL.QueryRef]
			queryFile := filepath.Base(query.File)
			queryBody := readQueryBody(in, query.File)
			variablesFile := DNSLabel(op.ID) + ".variables.json"
			matchersFile := DNSLabel(op.ID) + ".matchers.json"
			correlationID := graphqlCorrelationID(op)
			plan.Queries[queryFile] = queryBody
			plan.Variables[variablesFile] = renderStringMapJSON(op.GraphQL.Variables, in.RunID, correlationID, plan.Params)
			plan.Matchers[matchersFile] = renderMatchersJSON(op.GraphQL.Match, in.RunID, correlationID, plan.Params)
			step := generatedStep{
				Ordinal:     ordinal,
				OperationID: op.ID,
				Type:        op.Type,
				ApplyFile:   stepFile(ordinal, op.ID),
				AssertFile:  assertFile(ordinal, op.ID),
				Job:         graphqlExpectJob(in, scenarioSlug, ordinal, op, queryFile, variablesFile, matchersFile),
				Assert:      probeJobAssert(in, scenarioSlug, ordinal, op.ID),
			}
			plan.Steps = append(plan.Steps, step)
			ordinal++
		case "mongodb.expect":
			filterFile := DNSLabel(op.ID) + ".filter.json"
			matchersFile := DNSLabel(op.ID) + ".matchers.json"
			plan.Variables[filterFile] = renderJSONTemplate(op.MongoDB.Filter, in.RunID, op.MongoDB.CorrelationID, plan.Params)
			plan.Matchers[matchersFile] = renderMatchersJSON(op.MongoDB.Match, in.RunID, op.MongoDB.CorrelationID, plan.Params)
			step := generatedStep{
				Ordinal:     ordinal,
				OperationID: op.ID,
				Type:        op.Type,
				ApplyFile:   stepFile(ordinal, op.ID),
				AssertFile:  assertFile(ordinal, op.ID),
				Job:         mongodbExpectJob(in, scenarioSlug, ordinal, op, filterFile, matchersFile),
				Assert:      probeJobAssert(in, scenarioSlug, ordinal, op.ID),
			}
			plan.Steps = append(plan.Steps, step)
			ordinal++
		case "postgresql.expect":
			queryFile := DNSLabel(op.ID) + ".sql"
			argsFile := DNSLabel(op.ID) + ".args.json"
			matchersFile := DNSLabel(op.ID) + ".matchers.json"
			plan.Variables[queryFile] = renderTemplate(op.Postgres.Query, in.RunID, op.Postgres.CorrelationID, plan.Params)
			plan.Variables[argsFile] = renderStringSliceJSON(op.Postgres.Args, in.RunID, op.Postgres.CorrelationID, plan.Params)
			plan.Matchers[matchersFile] = renderMatchersJSON(op.Postgres.Match, in.RunID, op.Postgres.CorrelationID, plan.Params)
			step := generatedStep{
				Ordinal:     ordinal,
				OperationID: op.ID,
				Type:        op.Type,
				ApplyFile:   stepFile(ordinal, op.ID),
				AssertFile:  assertFile(ordinal, op.ID),
				Job:         postgresqlExpectJob(in, scenarioSlug, ordinal, op, queryFile, argsFile, matchersFile),
				Assert:      probeJobAssert(in, scenarioSlug, ordinal, op.ID),
			}
			plan.Steps = append(plan.Steps, step)
			ordinal++
		}
	}
	return plan
}

func generatedReadme(in Inputs) string {
	var b strings.Builder
	b.WriteString("# Generated workspace\n\n")
	b.WriteString(fmt.Sprintf("Scenario: `%s`\n", in.ScenarioName))
	b.WriteString(fmt.Sprintf("Run ID: `%s`\n", in.RunID))
	b.WriteString(fmt.Sprintf("Namespace: `%s`\n", in.Namespace))
	if in.KubeContext != "" {
		b.WriteString(fmt.Sprintf("Kube context: `%s`\n", in.KubeContext))
	}
	b.WriteString("\nThis directory is generated by `spex compile`.\n\n")
	b.WriteString("Run it with:\n\n")
	b.WriteString("```sh\n")
	b.WriteString("spex run --workspace .\n")
	b.WriteString("```\n")
	if in.Integration != nil {
		b.WriteString("\nIntegration profile: enabled.\n")
		if in.Integration.Spec.KIND.Start {
			b.WriteString("\nKUTTL is configured to create the kind cluster through `startKIND: true`.\n")
		}
		if in.Integration.Spec.KIND.Config != "" {
			b.WriteString("The kind config was copied to `kind.yaml`.\n")
		}
		if len(in.Integration.Spec.KIND.Commands) > 0 {
			b.WriteString("Top-level KUTTL commands prepare images before tests run.\n")
		}
		if integrationSetupEnabled(in) {
			b.WriteString("The generated `01-integration-setup.yaml` step installs the configured real services.\n")
		}
	}
	return b.String()
}

func integrationSetupEnabled(in Inputs) bool {
	return hasMaterializedSecrets(in) || (in.Integration != nil && (len(in.Integration.Spec.Setup.Commands) > 0 || len(in.Integration.Spec.HelmApps) > 0))
}

func staticStepOrdinal(in Inputs) int {
	if integrationSetupEnabled(in) {
		return 2
	}
	return 1
}

func firstProbeOrdinal(in Inputs) int {
	return staticStepOrdinal(in) + 1
}

type integrationRenderContext struct {
	WorkspaceDir          string
	RepoRoot              string
	IntegrationProfileDir string
}

func kuttlTest(in Inputs, ctx integrationRenderContext) string {
	startKIND := in.StartKIND
	var b strings.Builder
	b.WriteString("apiVersion: kuttl.dev/v1beta1\nkind: TestSuite\n")
	if in.Integration != nil {
		kindSpec := in.Integration.Spec.KIND
		if kindSpec.Start {
			startKIND = true
		}
		if len(kindSpec.Containers) > 0 {
			b.WriteString("kindContainers:\n")
			for _, container := range kindSpec.Containers {
				b.WriteString(fmt.Sprintf("  - %s\n", yamlString(renderIntegrationValue(in, ctx, container))))
			}
		}
		if kindSpec.Config != "" {
			b.WriteString("kindConfig: ./kind.yaml\n")
		}
		if kindSpec.Start && in.KubeContext != "" {
			b.WriteString(fmt.Sprintf("kindContext: %s\n", yamlString(in.KubeContext)))
		}
		if kindSpec.NodeCache != nil {
			b.WriteString(fmt.Sprintf("kindNodeCache: %t\n", *kindSpec.NodeCache))
		}
		if len(kindSpec.Commands) > 0 {
			b.WriteString("commands:\n")
			writeKUTTLCommands(&b, in, ctx, kindSpec.Commands)
		}
	}
	b.WriteString(fmt.Sprintf("testDirs:\n  - kuttl\nartifactsDir: artifacts/kuttl\nreportFormat: xml\nnamespace: %s\nparallel: 1\ntimeout: %d\nstartKIND: %t\nskipDelete: true\nskipClusterDelete: true\nsuppress:\n  - events\n", yamlString(in.Namespace), kuttlSuiteTimeout(in), startKIND))
	return b.String()
}

func kuttlSuiteTimeout(in Inputs) int {
	timeout := 120
	if in.Integration == nil {
		return timeout
	}
	for _, command := range append(in.Integration.Spec.KIND.Commands, in.Integration.Spec.Setup.Commands...) {
		if command.Timeout > timeout {
			timeout = command.Timeout
		}
	}
	for _, app := range in.Integration.Spec.HelmApps {
		if app.Timeout == "" {
			continue
		}
		duration, err := time.ParseDuration(app.Timeout)
		if err == nil && int(duration.Seconds()) > timeout {
			timeout = int(duration.Seconds())
		}
	}
	return timeout
}

func executionPlan(plan generationPlan) string {
	var b strings.Builder
	b.WriteString("steps:\n  - 00 rerun cleanup\n")
	if len(plan.Steps) > 0 && plan.Steps[0].Ordinal > 2 {
		b.WriteString("  - 01 integration setup\n  - 02 apply RBAC when enabled and static payload/query/variables/matcher ConfigMaps\n")
	} else {
		b.WriteString("  - 01 apply RBAC when enabled and static payload/query/variables/matcher ConfigMaps\n")
	}
	for _, step := range plan.Steps {
		b.WriteString(fmt.Sprintf("  - %02d %s\n", step.Ordinal, step.OperationID))
	}
	return b.String()
}

func integrationSetupStep(in Inputs, ctx integrationRenderContext) string {
	var b strings.Builder
	b.WriteString("apiVersion: kuttl.dev/v1beta1\nkind: TestStep\ncommands:\n")
	if in.Integration != nil {
		writeKUTTLCommands(&b, in, ctx, in.Integration.Spec.Setup.Commands)
		writeKUTTLCommands(&b, in, ctx, helmAppCommands(in))
	}
	writeKUTTLCommands(&b, in, ctx, secretMaterializationCommands(in, ctx))
	return b.String()
}

func hasMaterializedSecrets(in Inputs) bool {
	if isSSMReference(in.Binding.Spec.MQTT.BrokerURL) {
		return true
	}
	for _, secret := range in.Binding.Spec.Secrets {
		if secret.Type == "localEnvFile" || secret.Type == "awsSsmParameter" {
			return true
		}
	}
	return false
}

func secretMaterializationCommands(in Inputs, ctx integrationRenderContext) []KUTTLCommand {
	var ids []string
	for id, secret := range in.Binding.Spec.Secrets {
		if secret.Type == "localEnvFile" || secret.Type == "awsSsmParameter" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var commands []KUTTLCommand
	for _, id := range ids {
		secret := in.Binding.Spec.Secrets[id]
		switch secret.Type {
		case "localEnvFile":
			commands = append(commands, KUTTLCommand{Command: localEnvSecretCommand(in, ctx, id, secret), Timeout: 60})
		case "awsSsmParameter":
			commands = append(commands, KUTTLCommand{Command: ssmSecretCommand(in, ctx, id, secret), Timeout: 120})
		}
	}
	if isSSMReference(in.Binding.Spec.MQTT.BrokerURL) {
		commands = append(commands, KUTTLCommand{Command: ssmMQTTBrokerURLCommand(in, ctx), Timeout: 120})
	}
	return commands
}

func ssmMQTTBrokerURLCommand(in Inputs, ctx integrationRenderContext) string {
	secret := Secret{
		Name: mqttBrokerURLSecretName(in.Binding),
		Keys: map[string]string{"brokerURL": "brokerURL"},
	}
	var b strings.Builder
	b.WriteString("set -eu\n")
	b.WriteString(fmt.Sprintf("SPEX_SSM_MQTT_BROKER_URL=$(aws ssm get-parameter --with-decryption --name %s --query Parameter.Value --output text)\n", shellQuote(ssmParameterName(in.Binding.Spec.MQTT.BrokerURL))))
	writeSecretCreatePipelineWithLabels(&b, in, ctx, "mqtt-broker-url", secret, []string{"spex/source=aws-ssm"}, func(logicalKey string) string {
		return `"${SPEX_SSM_MQTT_BROKER_URL}"`
	})
	return b.String()
}

func localEnvSecretCommand(in Inputs, ctx integrationRenderContext, id string, secret Secret) string {
	envFile := secret.EnvFile
	if !filepath.IsAbs(envFile) && in.BindingPath != "" {
		envFile = filepath.Join(filepath.Dir(in.BindingPath), envFile)
	}
	var b strings.Builder
	b.WriteString("set -eu\n")
	b.WriteString("set -a\n")
	b.WriteString(". " + shellQuote(filepath.ToSlash(envFile)) + "\n")
	b.WriteString("set +a\n")
	writeSecretCreatePipeline(&b, in, ctx, id, secret, func(logicalKey string) string {
		envName := secret.Env[logicalKey]
		if envName == "" {
			envName = defaultSecretEnvName(id, logicalKey)
		}
		return fmt.Sprintf(`"${%s}"`, envName)
	})
	return b.String()
}

func ssmSecretCommand(in Inputs, ctx integrationRenderContext, id string, secret Secret) string {
	var b strings.Builder
	b.WriteString("set -eu\n")
	var logicalKeys []string
	for logicalKey := range secret.Keys {
		logicalKeys = append(logicalKeys, logicalKey)
	}
	sort.Strings(logicalKeys)
	for _, logicalKey := range logicalKeys {
		varName := "SPEX_SSM_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(logicalKey))
		b.WriteString(fmt.Sprintf("%s=$(aws ssm get-parameter --with-decryption --name %s --query Parameter.Value --output text)\n", varName, shellQuote(ssmParameterName(secret.SSMParameters[logicalKey]))))
	}
	writeSecretCreatePipelineWithLabels(&b, in, ctx, id, secret, []string{"spex/source=aws-ssm"}, func(logicalKey string) string {
		varName := "SPEX_SSM_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(logicalKey))
		return fmt.Sprintf(`"${%s}"`, varName)
	})
	return b.String()
}

func ssmParameterName(value string) string {
	if match := ssmReferencePattern.FindStringSubmatch(value); match != nil {
		return match[1]
	}
	return strings.TrimSpace(value)
}

func isSSMReference(value string) bool {
	return ssmReferencePattern.FindStringSubmatch(value) != nil
}

func mqttBrokerURLSecretName(binding TargetBinding) string {
	if binding.Spec.MQTT.CredentialsRef != "" {
		if secret := binding.Spec.Secrets[binding.Spec.MQTT.CredentialsRef]; secret.Name != "" {
			return secret.Name + "-broker-url"
		}
	}
	return "spex-mqtt-broker-url"
}

func writeSecretCreatePipeline(b *strings.Builder, in Inputs, ctx integrationRenderContext, id string, secret Secret, valueExpr func(string) string) {
	writeSecretCreatePipelineWithLabels(b, in, ctx, id, secret, nil, valueExpr)
}

func writeSecretCreatePipelineWithLabels(b *strings.Builder, in Inputs, ctx integrationRenderContext, id string, secret Secret, extraLabels []string, valueExpr func(string) string) {
	args := []string{"-n", in.Namespace, "create", "secret", "generic", secret.Name, "--dry-run=client", "-o", "yaml"}
	args = append(kubectlContextArgs(in, filepath.ToSlash(filepath.Join(ctx.WorkspaceDir, "kubeconfig"))), args...)
	b.WriteString("kubectl " + shellCommand(args...) + " \\\n")
	var logicalKeys []string
	for logicalKey := range secret.Keys {
		logicalKeys = append(logicalKeys, logicalKey)
	}
	sort.Strings(logicalKeys)
	for _, logicalKey := range logicalKeys {
		b.WriteString("  --from-literal=" + shellQuote(secret.Keys[logicalKey]) + "=" + valueExpr(logicalKey) + " \\\n")
	}
	labelArgs := []string{"label", "--local", "-f", "-", "-o", "yaml", "spex/owned=true", "spex/secret-id=" + id, "spex/run-id=" + in.RunID}
	labelArgs = append(labelArgs, extraLabels...)
	b.WriteString("  | kubectl " + shellCommand(labelArgs...) + " \\\n")
	createArgs := []string{"create", "-f", "-"}
	createArgs = append(kubectlContextArgs(in, filepath.ToSlash(filepath.Join(ctx.WorkspaceDir, "kubeconfig"))), createArgs...)
	b.WriteString("  | kubectl " + shellCommand(createArgs...) + "\n")
}

func helmAppCommands(in Inputs) []KUTTLCommand {
	if in.Integration == nil {
		return nil
	}
	var commands []KUTTLCommand
	for _, app := range in.Integration.Spec.HelmApps {
		namespace := app.Namespace
		if namespace == "" {
			namespace = in.Namespace
		}
		args := []string{"helm", "upgrade", "--install", app.Name, app.Chart, "--namespace", namespace, "--create-namespace"}
		args = append(args, kubectlContextArgs(in, "${kubeconfig}")...)
		if app.Repo != "" {
			args = append(args, "--repo", app.Repo)
		}
		for _, values := range app.Values {
			args = append(args, "--values", values)
		}
		for key, value := range app.Set {
			args = append(args, "--set", key+"="+value)
		}
		wait := true
		if app.Wait != nil {
			wait = *app.Wait
		}
		if wait {
			args = append(args, "--wait")
		}
		timeout := 300
		if app.Timeout != "" {
			if duration, err := time.ParseDuration(app.Timeout); err == nil {
				args = append(args, "--timeout", fmt.Sprintf("%ds", int(duration.Seconds())))
				timeout = int(duration.Seconds())
			}
		}
		commands = append(commands, KUTTLCommand{Command: "set -eu\n" + shellCommand(args...), Timeout: timeout})
	}
	return commands
}

func writeKUTTLCommands(b *strings.Builder, in Inputs, ctx integrationRenderContext, commands []KUTTLCommand) {
	for _, command := range commands {
		rendered := renderIntegrationValue(in, ctx, strings.TrimSpace(command.Command))
		if integrationCommandNeedsShell(rendered) {
			b.WriteString("  - script: |\n")
		} else {
			b.WriteString("  - command: >\n")
		}
		for _, line := range strings.Split(rendered, "\n") {
			b.WriteString("      " + line + "\n")
		}
		if command.Timeout > 0 {
			b.WriteString(fmt.Sprintf("    timeout: %d\n", command.Timeout))
		}
	}
}

func integrationCommandNeedsShell(command string) bool {
	return strings.ContainsAny(command, "|;\n") || strings.Contains(command, "&&") || strings.Contains(command, "||") || strings.HasPrefix(strings.TrimSpace(command), "set ")
}

func renderIntegrationValue(in Inputs, ctx integrationRenderContext, value string) string {
	probeImage := in.Binding.Spec.Probe.Image
	if probeImage == "" {
		probeImage = "spex-probe:dev"
	}
	probeImagePullPolicy := in.Binding.Spec.Probe.ImagePullPolicy
	if probeImagePullPolicy == "" {
		probeImagePullPolicy = "IfNotPresent"
	}
	replacements := map[string]string{
		"${workspaceDir}":          filepath.ToSlash(ctx.WorkspaceDir),
		"${kubeconfig}":            filepath.ToSlash(filepath.Join(ctx.WorkspaceDir, "kubeconfig")),
		"${repoRoot}":              filepath.ToSlash(ctx.RepoRoot),
		"${integrationProfileDir}": filepath.ToSlash(ctx.IntegrationProfileDir),
		"${namespace}":             in.Namespace,
		"${probeImage}":            probeImage,
		"${probeImagePullPolicy}":  probeImagePullPolicy,
		"${kubeContext}":           in.KubeContext,
		"${kubeContextArgs}":       strings.Join(kubectlContextArgs(in, filepath.ToSlash(filepath.Join(ctx.WorkspaceDir, "kubeconfig"))), " "),
		"${kindCluster}":           kindClusterName(in),
	}
	out := value
	for placeholder, replacement := range replacements {
		out = strings.ReplaceAll(out, placeholder, replacement)
	}
	return out
}

func kindClusterName(in Inputs) string {
	if in.Integration != nil && in.Integration.Spec.KIND.Start && in.KubeContext != "" {
		return in.KubeContext
	}
	if in.Integration != nil && in.Integration.Spec.KIND.ClusterName != "" {
		return in.Integration.Spec.KIND.ClusterName
	}
	if strings.HasPrefix(in.KubeContext, "kind-") && len(in.KubeContext) > len("kind-") {
		return strings.TrimPrefix(in.KubeContext, "kind-")
	}
	return "kind"
}

func stepMap(in Inputs, plan generationPlan) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`apiVersion: spex.stepmap.v0.1
kind: StepMap
metadata:
  scenario: %s
  runId: %s
spec:
  scenarioFile: %s
  bindingFile: %s
  catalogFiles:
%s
  namespace: %s
  kubeContext: %s
  steps:
`, yamlString(plan.ScenarioSlug), yamlString(in.RunID), yamlString(filepath.ToSlash(in.ScenarioPath)), yamlString(filepath.ToSlash(in.BindingPath)), catalogFilesYAML(in.CatalogPaths), yamlString(in.Namespace), yamlString(in.KubeContext)))
	for _, step := range plan.Steps {
		applyFile := filepath.ToSlash(filepath.Join("kuttl", plan.ScenarioSlug, step.ApplyFile))
		assertFile := filepath.ToSlash(filepath.Join("kuttl", plan.ScenarioSlug, step.AssertFile))
		b.WriteString(fmt.Sprintf("    - ordinal: %d\n      operationId: %s\n      operationType: %s\n      jobName: %s\n      podSelector:\n        spex/run-id: %s\n        spex/operation-id: %s\n        spex/step-ordinal: %s\n      generatedFiles:\n        - %s\n        - %s\n", step.Ordinal, yamlString(step.OperationID), yamlString(step.Type), yamlString(jobName(plan.ScenarioSlug, step.Ordinal, step.OperationID)), yamlString(in.RunID), yamlString(DNSLabel(step.OperationID)), yamlString(twoDigitOrdinal(step.Ordinal)), yamlString(applyFile), yamlString(assertFile)))
	}
	return b.String()
}

func catalogFilesYAML(paths []string) string {
	if len(paths) == 0 {
		return "    []"
	}
	var b strings.Builder
	for _, path := range paths {
		b.WriteString("    - ")
		b.WriteString(yamlString(filepath.ToSlash(path)))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func cleanupStep(in Inputs, scenarioSlug string, ctx integrationRenderContext) string {
	contextArgs := kubectlContextArgs(in, filepath.ToSlash(filepath.Join(ctx.WorkspaceDir, "kubeconfig")))
	selector := "spex/owned=true,spex/scenario=" + scenarioSlug
	jobDelete := shellCommand(append(append([]string{"kubectl"}, contextArgs...), "-n", in.Namespace, "delete", "job", "-l", selector, "--ignore-not-found=true")...)
	configMapDelete := shellCommand(append(append([]string{"kubectl"}, contextArgs...), "-n", in.Namespace, "delete", "configmap", "-l", selector+",spex/runtime=true", "--ignore-not-found=true")...)
	return fmt.Sprintf("apiVersion: kuttl.dev/v1beta1\nkind: TestStep\ncommands:\n  - script: |\n      %s\n      %s\n", jobDelete, configMapDelete)
}

func kubectlContextArgs(in Inputs, kubeconfig string) []string {
	if kubeconfig != "" {
		return []string{"--kubeconfig", kubeconfig}
	}
	if in.KubeContext != "" {
		return []string{"--context", in.KubeContext}
	}
	return nil
}

func staticConfigMaps(in Inputs, plan generationPlan) string {
	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
  labels:
    spex/owned: "true"
    spex/scenario: "%s"
    spex/run-id: "%s"
    spex/static: "true"
data:
%s---
apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
  labels:
    spex/owned: "true"
    spex/scenario: "%s"
    spex/run-id: "%s"
    spex/static: "true"
data:
%s---
apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
  labels:
    spex/owned: "true"
    spex/scenario: "%s"
    spex/run-id: "%s"
    spex/static: "true"
data:
%s---
apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
  labels:
    spex/owned: "true"
    spex/scenario: "%s"
    spex/run-id: "%s"
    spex/static: "true"
data:
%s`, yamlString(payloadsConfigMapName(plan.ScenarioSlug)), yamlString(in.Namespace), plan.ScenarioSlug, in.RunID, configMapData(plan.Payloads),
		yamlString(graphqlConfigMapName(plan.ScenarioSlug)), yamlString(in.Namespace), plan.ScenarioSlug, in.RunID, configMapData(plan.Queries),
		yamlString(variablesConfigMapName(plan.ScenarioSlug)), yamlString(in.Namespace), plan.ScenarioSlug, in.RunID, configMapData(plan.Variables),
		yamlString(matchersConfigMapName(plan.ScenarioSlug)), yamlString(in.Namespace), plan.ScenarioSlug, in.RunID, configMapData(plan.Matchers))
}

func redpandaSnapshotJob(in Inputs, scenarioSlug string, ordinal int) string {
	args := []string{
		"redpanda", "snapshot-offsets",
		"--brokers=" + in.Binding.Spec.Redpanda.Brokers,
		"--offsets-configmap=" + offsetConfigMapName(scenarioSlug),
		"--namespace=" + in.Namespace,
		"--scenario=" + scenarioSlug,
		"--run-id=" + in.RunID,
		"--timeout=" + defaultTimeout(in),
	}
	for _, topic := range redpandaSnapshotTopics(in) {
		args = append(args, "--topic="+topic)
	}
	return probeJob(in, scenarioSlug, ordinal, "redpanda-snapshot-offsets", "redpanda.snapshotOffsets", args)
}

func mqttPublishJob(in Inputs, scenarioSlug string, ordinal int, op Operation, payloadFile string, params map[string]string) string {
	topic := renderTemplate(op.MQTT.Topic, in.RunID, op.MQTT.CorrelationID, params)
	args := []string{
		"mqtt", "publish",
		"--topic=" + topic,
		"--client-id=" + mqttClientID(in, scenarioSlug, ordinal, op.ID),
		"--qos=1",
		"--payload-file=/spex/payloads/" + payloadFile,
		"--timeout=" + defaultTimeout(in),
	}
	if !isSSMReference(in.Binding.Spec.MQTT.BrokerURL) {
		args = append([]string{"mqtt", "publish", "--broker-url=" + in.Binding.Spec.MQTT.BrokerURL}, args[2:]...)
	}
	return probeJob(in, scenarioSlug, ordinal, op.ID, op.Type, args)
}

func mqttClientID(in Inputs, scenarioSlug string, ordinal int, operationID string) string {
	prefix := in.Binding.Spec.MQTT.ClientIDPrefix
	if prefix == "" {
		prefix = "spex"
	}
	return DNSLabel(prefix + "-" + scenarioSlug + "-" + in.RunID + "-" + twoDigitOrdinal(ordinal) + "-" + operationID)
}

func redpandaContainsJob(in Inputs, scenarioSlug string, ordinal int, op Operation, matchersFile string) string {
	topic := redpandaTopicName(in, op.Redpanda.TopicRef)
	return probeJob(in, scenarioSlug, ordinal, op.ID, op.Type, []string{
		"redpanda", "contains",
		"--brokers=" + in.Binding.Spec.Redpanda.Brokers,
		"--topic=" + topic,
		"--offsets-configmap=" + offsetConfigMapName(scenarioSlug),
		"--namespace=" + in.Namespace,
		"--scenario=" + scenarioSlug,
		"--run-id=" + in.RunID,
		"--matchers-file=/spex/matchers/" + matchersFile,
		"--timeout=" + redpandaTimeout(in, op),
		"--poll-interval=" + defaultPollInterval(in),
	})
}

func graphqlExpectJob(in Inputs, scenarioSlug string, ordinal int, op Operation, queryFile, variablesFile, matchersFile string) string {
	args := []string{
		"graphql", "expect",
		"--endpoint=" + in.Binding.Spec.GraphQL.Endpoint,
		"--query-file=/spex/graphql/" + queryFile,
		"--variables-file=/spex/variables/" + variablesFile,
		"--matchers-file=/spex/matchers/" + matchersFile,
		"--timeout=" + graphqlTimeout(in, op),
		"--poll-interval=" + defaultPollInterval(in),
	}
	if in.Binding.Spec.GraphQL.Auth.Type == "keycloakClientCredentials" {
		args = append(args,
			"--keycloak-token-url="+in.Binding.Spec.GraphQL.Auth.TokenURL,
			"--keycloak-client-id="+in.Binding.Spec.GraphQL.Auth.ClientID,
		)
		for _, scope := range in.Binding.Spec.GraphQL.Auth.Scopes {
			args = append(args, "--keycloak-scope="+scope)
		}
	}
	return probeJob(in, scenarioSlug, ordinal, op.ID, op.Type, args)
}

func mongodbExpectJob(in Inputs, scenarioSlug string, ordinal int, op Operation, filterFile, matchersFile string) string {
	return probeJob(in, scenarioSlug, ordinal, op.ID, op.Type, []string{
		"mongodb", "expect",
		"--uri=" + in.Binding.Spec.MongoDB.URI,
		"--database=" + in.Binding.Spec.MongoDB.Database,
		"--collection=" + op.MongoDB.Collection,
		"--filter-file=/spex/variables/" + filterFile,
		"--matchers-file=/spex/matchers/" + matchersFile,
		"--timeout=" + mongodbTimeout(in, op),
		"--poll-interval=" + defaultPollInterval(in),
	})
}

func postgresqlExpectJob(in Inputs, scenarioSlug string, ordinal int, op Operation, queryFile, argsFile, matchersFile string) string {
	return probeJob(in, scenarioSlug, ordinal, op.ID, op.Type, []string{
		"postgresql", "expect",
		"--uri=" + in.Binding.Spec.PostgreSQL.URI,
		"--query-file=/spex/variables/" + queryFile,
		"--args-file=/spex/variables/" + argsFile,
		"--matchers-file=/spex/matchers/" + matchersFile,
		"--timeout=" + postgresqlTimeout(in, op),
		"--poll-interval=" + defaultPollInterval(in),
	})
}

func rabbitmqPublishJob(in Inputs, scenarioSlug string, ordinal int, op Operation, payloadFile string, params map[string]string) string {
	exchange := renderTemplate(op.RabbitMQ.Exchange, in.RunID, op.RabbitMQ.CorrelationID, params)
	routingKey := renderTemplate(op.RabbitMQ.RoutingKey, in.RunID, op.RabbitMQ.CorrelationID, params)
	return probeJob(in, scenarioSlug, ordinal, op.ID, op.Type, []string{
		"rabbitmq", "publish",
		"--uri=" + in.Binding.Spec.RabbitMQ.URI,
		"--exchange=" + exchange,
		"--routing-key=" + routingKey,
		"--payload-file=/spex/payloads/" + payloadFile,
		"--timeout=" + rabbitmqTimeout(in, op),
	})
}

func rabbitmqExpectJob(in Inputs, scenarioSlug string, ordinal int, op Operation, matchersFile string, params map[string]string) string {
	queue := renderTemplate(op.RabbitMQ.Queue, in.RunID, op.RabbitMQ.CorrelationID, params)
	return probeJob(in, scenarioSlug, ordinal, op.ID, op.Type, []string{
		"rabbitmq", "expect",
		"--uri=" + in.Binding.Spec.RabbitMQ.URI,
		"--queue=" + queue,
		"--matchers-file=/spex/matchers/" + matchersFile,
		"--timeout=" + rabbitmqTimeout(in, op),
		"--poll-interval=" + defaultPollInterval(in),
	})
}

func probeJob(in Inputs, scenarioSlug string, ordinal int, operationID, operationType string, args []string) string {
	name := jobName(scenarioSlug, ordinal, operationID)
	operationSlug := DNSLabel(operationID)
	ordinalLabel := twoDigitOrdinal(ordinal)
	image := in.Binding.Spec.Probe.Image
	if image == "" {
		image = "spex-probe:dev"
	}
	imagePullPolicy := in.Binding.Spec.Probe.ImagePullPolicy
	if imagePullPolicy == "" {
		imagePullPolicy = "IfNotPresent"
	}
	serviceAccountName := probeServiceAccountName(in)
	return fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  namespace: %s
  labels:
    spex/owned: "true"
    spex/scenario: "%s"
    spex/operation-id: "%s"
    spex/operation-type: "%s"
    spex/step-ordinal: "%s"
    spex/run-id: "%s"
spec:
  backoffLimit: 0
  activeDeadlineSeconds: %d
  template:
    metadata:
      labels:
        spex/owned: "true"
        spex/scenario: "%s"
        spex/operation-id: "%s"
        spex/operation-type: "%s"
        spex/step-ordinal: "%s"
        spex/run-id: "%s"
    spec:
      restartPolicy: Never
      serviceAccountName: %s
      containers:
        - name: probe
          image: %s
          imagePullPolicy: %s
%s
          args:
%s
          volumeMounts:
            - name: payloads
              mountPath: /spex/payloads
              readOnly: true
            - name: graphql
              mountPath: /spex/graphql
              readOnly: true
            - name: variables
              mountPath: /spex/variables
              readOnly: true
            - name: matchers
              mountPath: /spex/matchers
              readOnly: true
            - name: results
              mountPath: /spex/results
      volumes:
        - name: payloads
          configMap:
            name: %s
        - name: graphql
          configMap:
            name: %s
        - name: variables
          configMap:
            name: %s
        - name: matchers
          configMap:
            name: %s
        - name: results
          emptyDir: {}
`, yamlString(name), yamlString(in.Namespace), scenarioSlug, operationSlug, operationType, ordinalLabel, in.RunID, activeDeadlineSeconds(args),
		scenarioSlug, operationSlug, operationType, ordinalLabel, in.RunID, yamlString(serviceAccountName), yamlString(image), yamlString(imagePullPolicy), secretEnv(in, args), yamlArgs(args),
		yamlString(payloadsConfigMapName(scenarioSlug)), yamlString(graphqlConfigMapName(scenarioSlug)), yamlString(variablesConfigMapName(scenarioSlug)), yamlString(matchersConfigMapName(scenarioSlug)))
}

func rbac(in Inputs, scenarioSlug string) string {
	serviceAccountName := probeServiceAccountName(in)
	return fmt.Sprintf(`apiVersion: v1
kind: ServiceAccount
metadata:
  name: %s
  namespace: %s
  labels:
    spex/owned: "true"
    spex/scenario: "%s"
    spex/run-id: "%s"
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: %s
  namespace: %s
  labels:
    spex/owned: "true"
    spex/scenario: "%s"
    spex/run-id: "%s"
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "create", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: %s
  namespace: %s
  labels:
    spex/owned: "true"
    spex/scenario: "%s"
    spex/run-id: "%s"
subjects:
  - kind: ServiceAccount
    name: %s
    namespace: %s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: %s
`, yamlString(serviceAccountName), yamlString(in.Namespace), scenarioSlug, in.RunID,
		yamlString(rbacName(scenarioSlug)), yamlString(in.Namespace), scenarioSlug, in.RunID,
		yamlString(rbacName(scenarioSlug)), yamlString(in.Namespace), scenarioSlug, in.RunID,
		yamlString(serviceAccountName), yamlString(in.Namespace), yamlString(rbacName(scenarioSlug)))
}

func probeJobAssert(in Inputs, scenarioSlug string, ordinal int, operationID string) string {
	return fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  namespace: %s
status:
  succeeded: 1
`, yamlString(jobName(scenarioSlug, ordinal, operationID)), yamlString(in.Namespace))
}

func probeServiceAccountName(in Inputs) string {
	if in.Binding.Spec.Probe.ServiceAccountName != "" {
		return in.Binding.Spec.Probe.ServiceAccountName
	}
	return "spex-probe"
}

func activeDeadlineSeconds(args []string) int {
	timeout := 30 * time.Second
	for _, arg := range args {
		value, ok := strings.CutPrefix(arg, "--timeout=")
		if !ok {
			continue
		}
		parsed, err := time.ParseDuration(value)
		if err == nil && parsed > 0 {
			timeout = parsed
		}
	}
	deadline := timeout + 30*time.Second
	seconds := int(deadline.Round(time.Second).Seconds())
	if seconds < 1 {
		return 1
	}
	return seconds
}

func readQueryBody(in Inputs, queryFile string) string {
	path := resolveScenarioFile(in.ScenarioPath, queryFile)
	content, err := readRegularInputFile(path, maxGraphQLQueryFileSize)
	if err != nil {
		return ""
	}
	return string(content)
}

func renderStringMapJSON(source map[string]string, runID, correlationID string, params map[string]string) string {
	rendered := map[string]string{}
	for key, value := range source {
		rendered[key] = renderTemplate(value, runID, correlationID, params)
	}
	content, err := json.MarshalIndent(rendered, "", "  ")
	if err != nil {
		return "{}\n"
	}
	return string(content) + "\n"
}

func renderStringSliceJSON(source []string, runID, correlationID string, params map[string]string) string {
	out := make([]string, 0, len(source))
	for _, value := range source {
		out = append(out, renderTemplate(value, runID, correlationID, params))
	}
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		panic(err)
	}
	return string(encoded) + "\n"
}

func renderMatchersJSON(source []Matcher, runID, correlationID string, params map[string]string) string {
	type renderedMatcher struct {
		Path         string `json:"path"`
		EqualsString string `json:"equalsString,omitempty"`
		EqualsNumber string `json:"equalsNumber,omitempty"`
		EqualsBool   *bool  `json:"equalsBool,omitempty"`
		EqualsNull   *bool  `json:"equalsNull,omitempty"`
	}
	rendered := make([]renderedMatcher, 0, len(source))
	for _, matcher := range source {
		rendered = append(rendered, renderedMatcher{
			Path:         matcher.Path,
			EqualsString: renderTemplate(matcher.EqualsString, runID, correlationID, params),
			EqualsNumber: renderTemplate(matcher.EqualsNumber, runID, correlationID, params),
			EqualsBool:   matcher.EqualsBool,
			EqualsNull:   matcher.EqualsNull,
		})
	}
	content, err := json.MarshalIndent(rendered, "", "  ")
	if err != nil {
		return "[]\n"
	}
	return string(content) + "\n"
}

func renderJSONTemplate(source, runID, correlationID string, params map[string]string) string {
	var parsed any
	decoder := json.NewDecoder(strings.NewReader(source))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return source
	}
	rendered := renderJSONTemplateValue(parsed, runID, correlationID, params)
	content, err := json.MarshalIndent(rendered, "", "  ")
	if err != nil {
		return source
	}
	return string(content) + "\n"
}

func renderJSONTemplateValue(value any, runID, correlationID string, params map[string]string) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = renderJSONTemplateValue(child, runID, correlationID, params)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = renderJSONTemplateValue(child, runID, correlationID, params)
		}
		return out
	case string:
		return renderTemplate(typed, runID, correlationID, params)
	default:
		return typed
	}
}

func renderTemplate(source, runID, correlationID string, params map[string]string) string {
	out := strings.ReplaceAll(source, "${scenarioRunId}", runID)
	out = strings.ReplaceAll(out, "${correlationId}", correlationID)
	for name, value := range params {
		out = strings.ReplaceAll(out, "${param."+name+"}", value)
	}
	return out
}

func resolvedParameters(in Inputs) map[string]string {
	params := map[string]string{}
	for name, parameter := range in.Scenario.Spec.Parameters {
		params[name] = parameter.Default
	}
	for name, value := range in.Binding.Spec.ScenarioParameters {
		params[name] = value
	}
	return params
}

func needsRedpandaSnapshot(ops []Operation) bool {
	for _, op := range ops {
		if op.Type == "redpanda.contains" {
			return true
		}
	}
	return false
}

func redpandaSnapshotTopics(in Inputs) []string {
	seen := map[string]bool{}
	var topics []string
	for _, op := range in.Scenario.Spec.Operations {
		if op.Type != "redpanda.contains" {
			continue
		}
		topic := redpandaTopicName(in, op.Redpanda.TopicRef)
		if !seen[topic] {
			seen[topic] = true
			topics = append(topics, topic)
		}
	}
	return topics
}

func graphqlCorrelationID(op Operation) string {
	if op.GraphQL == nil {
		return ""
	}
	if value := op.GraphQL.Variables["correlationId"]; value != "" {
		return value
	}
	return ""
}

func defaultTimeout(in Inputs) string {
	if in.Scenario.Spec.Defaults.Timeout != "" {
		return in.Scenario.Spec.Defaults.Timeout
	}
	return "30s"
}

func defaultPollInterval(in Inputs) string {
	if in.Scenario.Spec.Defaults.PollInterval != "" {
		return in.Scenario.Spec.Defaults.PollInterval
	}
	return "1s"
}

func redpandaTimeout(in Inputs, op Operation) string {
	if op.Redpanda != nil && op.Redpanda.Timeout != "" {
		return op.Redpanda.Timeout
	}
	return defaultTimeout(in)
}

func graphqlTimeout(in Inputs, op Operation) string {
	if op.GraphQL != nil && op.GraphQL.Timeout != "" {
		return op.GraphQL.Timeout
	}
	return defaultTimeout(in)
}

func mongodbTimeout(in Inputs, op Operation) string {
	if op.MongoDB != nil && op.MongoDB.Timeout != "" {
		return op.MongoDB.Timeout
	}
	return defaultTimeout(in)
}

func postgresqlTimeout(in Inputs, op Operation) string {
	if op.Postgres != nil && op.Postgres.Timeout != "" {
		return op.Postgres.Timeout
	}
	return defaultTimeout(in)
}

func rabbitmqTimeout(in Inputs, op Operation) string {
	if op.RabbitMQ != nil && op.RabbitMQ.Timeout != "" {
		return op.RabbitMQ.Timeout
	}
	return defaultTimeout(in)
}

func configMapData(files map[string]string) string {
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sortStrings(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(fmt.Sprintf("  %s: |\n%s", name, indent(files[name])))
	}
	return b.String()
}

func yamlArgs(args []string) string {
	var b strings.Builder
	for _, arg := range args {
		encoded, _ := json.Marshal(arg)
		b.WriteString("            - ")
		b.Write(encoded)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func yamlString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func shellCommand(args ...string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func secretEnv(in Inputs, args []string) string {
	switch {
	case len(args) >= 2 && args[0] == "mqtt" && args[1] == "publish":
		env := secretKeyEnv(in.Binding.Spec.Secrets[in.Binding.Spec.MQTT.CredentialsRef], map[string]string{
			"SPEX_MQTT_USERNAME": "username",
			"SPEX_MQTT_PASSWORD": "password",
		})
		if isSSMReference(in.Binding.Spec.MQTT.BrokerURL) {
			brokerEnv := secretKeyEnv(Secret{
				Name: mqttBrokerURLSecretName(in.Binding),
				Keys: map[string]string{"brokerURL": "brokerURL"},
			}, map[string]string{"SPEX_MQTT_BROKER_URL": "brokerURL"})
			if env == "" {
				return brokerEnv
			}
			return env + "\n" + strings.TrimPrefix(brokerEnv, "          env:\n")
		}
		return env
	case len(args) >= 2 && args[0] == "graphql" && args[1] == "expect":
		if in.Binding.Spec.GraphQL.Auth.Type == "keycloakClientCredentials" {
			return secretKeyEnv(in.Binding.Spec.Secrets[in.Binding.Spec.GraphQL.Auth.ClientSecretRef], map[string]string{
				"SPEX_GRAPHQL_KEYCLOAK_CLIENT_SECRET": "clientSecret",
			})
		}
		return secretKeyEnv(in.Binding.Spec.Secrets[in.Binding.Spec.GraphQL.CredentialsRef], map[string]string{
			"SPEX_GRAPHQL_TOKEN": "token",
		})
	case len(args) >= 2 && args[0] == "mongodb" && args[1] == "expect":
		return secretKeyEnv(in.Binding.Spec.Secrets[in.Binding.Spec.MongoDB.CredentialsRef], map[string]string{
			"SPEX_MONGODB_USERNAME": "username",
			"SPEX_MONGODB_PASSWORD": "password",
		})
	case len(args) >= 2 && args[0] == "postgresql" && args[1] == "expect":
		return secretKeyEnv(in.Binding.Spec.Secrets[in.Binding.Spec.PostgreSQL.CredentialsRef], map[string]string{
			"SPEX_POSTGRESQL_USERNAME": "username",
			"SPEX_POSTGRESQL_PASSWORD": "password",
		})
	case len(args) >= 2 && args[0] == "rabbitmq" && (args[1] == "publish" || args[1] == "expect"):
		return secretKeyEnv(in.Binding.Spec.Secrets[in.Binding.Spec.RabbitMQ.CredentialsRef], map[string]string{
			"SPEX_RABBITMQ_USERNAME": "username",
			"SPEX_RABBITMQ_PASSWORD": "password",
		})
	default:
		return ""
	}
}

func secretKeyEnv(secret Secret, envToKey map[string]string) string {
	if secret.Name == "" {
		return ""
	}
	var names []string
	for name := range envToKey {
		names = append(names, name)
	}
	sortStrings(names)
	var b strings.Builder
	b.WriteString("          env:\n")
	for _, name := range names {
		key := secret.Keys[envToKey[name]]
		b.WriteString(fmt.Sprintf(`            - name: %s
              valueFrom:
                secretKeyRef:
                  name: %s
                  key: %s
`, yamlString(name), yamlString(secret.Name), yamlString(key)))
	}
	return strings.TrimRight(b.String(), "\n")
}

func sortStrings(values []string) {
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}

func stepFile(ordinal int, operationID string) string {
	return fmt.Sprintf("%02d-op-%s.yaml", ordinal, DNSLabel(operationID))
}

func assertFile(ordinal int, operationID string) string {
	return fmt.Sprintf("%02d-assert.yaml", ordinal)
}

func jobName(scenarioSlug string, ordinal int, operationID string) string {
	return DNSLabel(fmt.Sprintf("spex-%s-%02d-%s", scenarioSlug, ordinal, operationID))
}

func twoDigitOrdinal(ordinal int) string {
	return fmt.Sprintf("%02d", ordinal)
}

func rbacName(scenarioSlug string) string {
	return DNSLabel("spex-" + scenarioSlug)
}

func payloadsConfigMapName(scenarioSlug string) string {
	return DNSLabel("spex-" + scenarioSlug + "-payloads")
}

func graphqlConfigMapName(scenarioSlug string) string {
	return DNSLabel("spex-" + scenarioSlug + "-graphql")
}

func variablesConfigMapName(scenarioSlug string) string {
	return DNSLabel("spex-" + scenarioSlug + "-variables")
}

func matchersConfigMapName(scenarioSlug string) string {
	return DNSLabel("spex-" + scenarioSlug + "-matchers")
}

func redpandaTopicName(in Inputs, topicRef string) string {
	topic, ok := in.Binding.Spec.Redpanda.Topics[topicRef]
	if !ok {
		return topicRef
	}
	return topic.Name
}

func offsetConfigMapName(scenarioSlug string) string {
	return DNSLabel("spex-" + scenarioSlug + "-redpanda-offsets")
}

func indent(s string) string {
	out := ""
	for _, line := range splitLines(s) {
		out += "    " + line + "\n"
	}
	return out
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, r := range s {
		if r == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
