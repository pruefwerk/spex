package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadInputsAcceptsLocalEnvFileSecretMaterialization(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "localEnvFile", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `      keys:
        username: username
        password: password`, `      envFile: local.env
      env:
        username: SPEX_MQTT_USERNAME
        password: SPEX_MQTT_PASSWORD
      keys:
        username: username
        password: password`, 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	if inputs.Binding.Spec.Secrets["mqtt-credentials"].EnvFile != "local.env" {
		t.Fatalf("local env file was not loaded")
	}
}

func TestLoadInputsAcceptsAWSSSMSecretMaterialization(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "awsSsmParameter", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `      keys:
        username: username
        password: password`, `      ssmParameters:
        username: '{{ ssm "team/dev/mqtt/username" }}'
        password: /team/dev/mqtt/password
      keys:
        username: username
        password: password`, 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	if inputs.Binding.Spec.Secrets["mqtt-credentials"].SSMParameters["password"] != "/team/dev/mqtt/password" {
		t.Fatalf("ssm parameter was not loaded")
	}
}

func TestLoadInputsRejectsSymlinkScenarioFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	realScenario := filepath.Join(t.TempDir(), "scenario.yaml")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realScenario, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(scenarioPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realScenario, scenarioPath); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected symlink scenario file error, got %v", err)
	}
}

func TestLoadInputsRejectsTrailingYAMLDocument(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	file, err := os.OpenFile(scenarioPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("---\n{}\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "unexpected trailing YAML document") {
		t.Fatalf("expected trailing YAML document error, got %v", err)
	}
}

func TestLoadInputsRejectsOversizedGraphQLQueryFile(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	queryDir := filepath.Join(dir, "examples", "queries")
	if err := os.MkdirAll(queryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("x", int(maxGraphQLQueryFileSize)+1)
	if err := os.WriteFile(filepath.Join(queryDir, "latest-device-reading.graphql"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "file is too large") {
		t.Fatalf("expected oversized query file error, got %v", err)
	}
}

func TestLoadScenarioSuiteResolvesGitBindingAndIntegrationProfileRefs(t *testing.T) {
	dir := t.TempDir()
	platformRepo := filepath.Join(dir, "platform-targets")
	if err := os.MkdirAll(platformRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(platformRepo, "dev-binding.yaml"), []byte(minimalBinding("kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(platformRepo, "kind-profile.yaml"), []byte(`apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  setup:
    commands:
      - command: kubectl version --client
        timeout: 30
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, platformRepo, "init", "-b", "main")
	runGitForTest(t, platformRepo, "add", ".")
	runGitForTest(t, platformRepo, "-c", "user.name=spex", "-c", "user.email=spex@example.invalid", "commit", "-m", "initial")

	scenarioRepo := filepath.Join(dir, "scenario-repo")
	if err := os.MkdirAll(filepath.Join(scenarioRepo, "scenarios"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(scenarioRepo, "examples", "queries"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scenarioRepo, "scenarios", "mqtt.yaml"), []byte(minimalScenario()), 0o644); err != nil {
		t.Fatal(err)
	}
	writeQuery(t, scenarioRepo, `query LatestDeviceReading($scenarioRunId: String!, $correlationId: String!, $deviceId: String!) {
  latestDeviceReading(scenarioRunId: $scenarioRunId, correlationId: $correlationId, deviceId: $deviceId) {
    scenarioRunId
    correlationId
  }
}`)
	suitePath := filepath.Join(scenarioRepo, "suite.yaml")
	platformURL := "file://" + filepath.ToSlash(platformRepo)
	if err := os.WriteFile(suitePath, []byte(`apiVersion: spex.suite.v0.1
kind: ScenarioSuite
metadata:
  name: git-backed-suite
spec:
  bindingRef: git::`+platformURL+`//dev-binding.yaml@main
  integrationProfileRef: git::`+platformURL+`//kind-profile.yaml@main
  scenarios:
    - scenarios/**/*.yaml
`), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, err := LoadScenarioSuite(suitePath)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.BindingPath == filepath.Join(platformRepo, "dev-binding.yaml") {
		t.Fatalf("expected binding to resolve through git cache, got source path")
	}
	if !strings.HasSuffix(filepath.ToSlash(resolved.BindingPath), "/dev-binding.yaml") {
		t.Fatalf("unexpected binding path %q", resolved.BindingPath)
	}
	if !strings.HasSuffix(filepath.ToSlash(resolved.IntegrationProfilePath), "/kind-profile.yaml") {
		t.Fatalf("unexpected integration profile path %q", resolved.IntegrationProfilePath)
	}
	if len(resolved.ScenarioPaths) != 1 {
		t.Fatalf("scenario count = %d", len(resolved.ScenarioPaths))
	}
}

func TestLoadScenarioSuiteResolvesLocalRefsToAbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "localEnvFile", "tcp://emqx.platform.svc.cluster.local:1883")
	profilePath := filepath.Join(dir, "kind-profile.yaml")
	if err := os.WriteFile(profilePath, []byte(`apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec: {}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	suitePath := filepath.Join(dir, "suite.yaml")
	if err := os.WriteFile(suitePath, []byte(`apiVersion: spex.suite.v0.1
kind: ScenarioSuite
metadata:
  name: local-suite
spec:
  bindingRef: binding.yaml
  integrationProfileRef: kind-profile.yaml
  scenarios:
    - scenario.yaml
`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(dir)
	resolved, err := LoadScenarioSuite("suite.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for field, path := range map[string]string{
		"suite":              resolved.Path,
		"binding":            resolved.BindingPath,
		"integrationProfile": resolved.IntegrationProfilePath,
		"scenario":           resolved.ScenarioPaths[0],
	} {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s path is not absolute: %q", field, path)
		}
	}
	if resolved.BindingPath != bindingPath {
		t.Fatalf("binding path = %q, want %q", resolved.BindingPath, bindingPath)
	}
	if resolved.ScenarioPaths[0] != scenarioPath {
		t.Fatalf("scenario path = %q, want %q", resolved.ScenarioPaths[0], scenarioPath)
	}
	if resolved.IntegrationProfilePath != profilePath {
		t.Fatalf("integration profile path = %q, want %q", resolved.IntegrationProfilePath, profilePath)
	}
}

func TestLoadInputsRejectsCredentialsInURLs(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://user:pass@emqx.platform.svc.cluster.local:1883")

	_, err := LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "embedded credentials") {
		t.Fatalf("expected embedded credentials error, got %v", err)
	}
}

func TestLoadInputsRejectsUnsupportedMQTTBrokerURLScheme(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "http://emqx.platform.svc.cluster.local:1883")

	_, err := LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), `spec.mqtt.brokerURL uses unsupported URL scheme "http"`) {
		t.Fatalf("expected unsupported MQTT scheme error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidMQTTClientIDPrefix(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "brokerURL: tcp://emqx.platform.svc.cluster.local:1883", "brokerURL: tcp://emqx.platform.svc.cluster.local:1883\n    clientIdPrefix: bad/prefix", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.mqtt.clientIdPrefix must match") {
		t.Fatalf("expected clientIdPrefix validation error, got %v", err)
	}
}

func TestLoadInputsAcceptsMongoDBAtlasBinding(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := filepath.Join(dir, "scenario.yaml")
	bindingPath := filepath.Join(dir, "binding.yaml")
	if err := os.WriteFile(scenarioPath, []byte(`apiVersion: spex.scenario.v0.1
kind: Scenario
metadata:
  name: mongodb-atlas-check
spec:
  operations:
    - id: assert-reading
      type: mongodb.expect
      mongodb:
        collection: readings
        filter: |
          {"scenarioRunId":"${scenarioRunId}","correlationId":"reading-1"}
        correlationId: reading-1
        match:
          - path: $.scenarioRunId
            equalsString: ${scenarioRunId}
          - path: $.correlationId
            equalsString: reading-1
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bindingPath, []byte(`apiVersion: spex.binding.v0.1
kind: TargetBinding
metadata:
  name: atlas
spec:
  namespace: spex-test
  rbac:
    create: true
  probe:
    image: spex-probe:dev
  secrets:
    atlas-credentials:
      type: kubernetesSecret
      name: atlas-credentials
      keys:
        username: username
        password: password
  mongodb:
    deployment: atlas
    uri: mongodb+srv://cluster.example.mongodb.net
    database: app
    credentialsRef: atlas-credentials
`), 0o644); err != nil {
		t.Fatal(err)
	}

	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatalf("expected Atlas binding to validate: %v", err)
	}
	if inputs.Binding.Spec.MongoDB.Deployment != "atlas" {
		t.Fatalf("expected atlas deployment, got %q", inputs.Binding.Spec.MongoDB.Deployment)
	}
}

func TestLoadInputsRejectsMongoDBAtlasWithoutCredentials(t *testing.T) {
	dir := t.TempDir()
	scenarioPath := filepath.Join(dir, "scenario.yaml")
	bindingPath := filepath.Join(dir, "binding.yaml")
	if err := os.WriteFile(scenarioPath, []byte(`apiVersion: spex.scenario.v0.1
kind: Scenario
metadata:
  name: mongodb-atlas-check
spec:
  operations:
    - id: assert-reading
      type: mongodb.expect
      mongodb:
        collection: readings
        filter: |
          {"scenarioRunId":"${scenarioRunId}","correlationId":"reading-1"}
        correlationId: reading-1
        match:
          - path: $.scenarioRunId
            equalsString: ${scenarioRunId}
          - path: $.correlationId
            equalsString: reading-1
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bindingPath, []byte(`apiVersion: spex.binding.v0.1
kind: TargetBinding
metadata:
  name: atlas
spec:
  namespace: spex-test
  rbac:
    create: true
  probe:
    image: spex-probe:dev
  secrets: {}
  mongodb:
    deployment: atlas
    uri: mongodb+srv://cluster.example.mongodb.net
    database: app
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.mongodb.credentialsRef is required") {
		t.Fatalf("expected missing Atlas credentials error, got %v", err)
	}
}

func TestLoadInputsRejectsGraphQLEndpointWithoutURLHost(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "endpoint: http://graphql-api.application.svc.cluster.local/graphql", "endpoint: /graphql", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.graphql.endpoint must include URL scheme and host") {
		t.Fatalf("expected invalid GraphQL endpoint error, got %v", err)
	}
}

func TestLoadInputsRejectsUnknownCredentialRef(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.ReplaceAll(string(content), "credentialsRef: mqtt-credentials", "credentialsRef: missing-secret"))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "references unknown secret") {
		t.Fatalf("expected unknown secret ref error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidNamespace(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "namespace: spex-test", "namespace: SPEX_test", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.namespace must be a DNS-1123 label") {
		t.Fatalf("expected namespace validation error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidServiceAccountName(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "serviceAccountName: spex-probe", "serviceAccountName: SPEX_probe", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.probe.serviceAccountName must be a DNS-1123 subdomain") {
		t.Fatalf("expected service account validation error, got %v", err)
	}
}

func TestLoadInputsRequiresServiceAccountWhenRBACCreateFalse(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "create: true", "create: false", 1))
	content = []byte(strings.Replace(string(content), "    serviceAccountName: spex-probe\n", "", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "serviceAccountName is required when spec.rbac.create is false") {
		t.Fatalf("expected service account requirement error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidImagePullPolicy(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "imagePullPolicy: IfNotPresent", "imagePullPolicy: Sometimes", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "imagePullPolicy must be one of") {
		t.Fatalf("expected imagePullPolicy validation error, got %v", err)
	}
}

func TestLoadInputsAcceptsMQTTOnlyScenarioWithoutRedpandaOrGraphQLBinding(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	scenarioContent, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	scenarioContent = []byte(strings.Split(string(scenarioContent), "    - id: assert-reading-1-in-redpanda")[0])
	if err := os.WriteFile(scenarioPath, scenarioContent, 0o644); err != nil {
		t.Fatal(err)
	}
	bindingContent, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	bindingContent = []byte(strings.Split(string(bindingContent), "  redpanda:")[0])
	if err := os.WriteFile(bindingPath, bindingContent, 0o644); err != nil {
		t.Fatal(err)
	}

	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs.Scenario.Spec.Operations) != 1 {
		t.Fatalf("operation count = %d", len(inputs.Scenario.Spec.Operations))
	}
}

func TestLoadInputsRejectsMissingMQTTBrokerWhenMQTTOperationExists(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "")

	_, err := LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.mqtt.brokerURL is required") {
		t.Fatalf("expected missing MQTT broker error, got %v", err)
	}
}

func TestLoadInputsRejectsMissingGraphQLEndpointWhenGraphQLOperationExists(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "    endpoint: http://graphql-api.application.svc.cluster.local/graphql\n", "", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.graphql.endpoint is required") {
		t.Fatalf("expected missing GraphQL endpoint error, got %v", err)
	}
}

func TestLoadInputsRejectsMissingGraphQLBearerTokenRef(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "    credentialsRef: graphql-token\n", "", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.graphql.credentialsRef is required") {
		t.Fatalf("expected missing GraphQL credentialsRef error, got %v", err)
	}
}

func TestLoadInputsRejectsMissingRedpandaBrokersWhenRedpandaOperationExists(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "    brokers: redpanda.streaming.svc.cluster.local:9092\n", "", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.redpanda.brokers is required") {
		t.Fatalf("expected missing Redpanda brokers error, got %v", err)
	}
}

func TestLoadInputsRejectsMalformedRedpandaBrokers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   string
		wantErr string
	}{
		{
			name:    "empty segment",
			value:   "redpanda.streaming.svc.cluster.local:9092,",
			wantErr: "contains empty broker",
		},
		{
			name:    "url",
			value:   "tcp://redpanda.streaming.svc.cluster.local:9092",
			wantErr: "must be host:port, not a URL",
		},
		{
			name:    "missing port",
			value:   "redpanda.streaming.svc.cluster.local",
			wantErr: "must be host:port",
		},
		{
			name:    "bad port",
			value:   "redpanda.streaming.svc.cluster.local:99999",
			wantErr: "has invalid port",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
			content, err := os.ReadFile(bindingPath)
			if err != nil {
				t.Fatal(err)
			}
			content = []byte(strings.Replace(string(content), "redpanda.streaming.svc.cluster.local:9092", tc.value, 1))
			if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
				t.Fatal(err)
			}

			_, err = LoadInputs(scenarioPath, bindingPath)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q error, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoadIntegrationProfileRejectsUnknownPlaceholder(t *testing.T) {
	dir := t.TempDir()
	kindConfigPath := filepath.Join(dir, "kind.yaml")
	if err := os.WriteFile(kindConfigPath, []byte("kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(dir, "profile.yaml")
	content := `apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  kind:
    start: true
    config: kind.yaml
    containers:
      - ${probeImgae}
`
	if err := os.WriteFile(profilePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadIntegrationProfile(profilePath)
	if err == nil || !strings.Contains(err.Error(), `unsupported integration placeholder "probeImgae"`) {
		t.Fatalf("expected unsupported placeholder error, got %v", err)
	}
}

func TestLoadIntegrationProfileRejectsInvalidKINDClusterName(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.yaml")
	content := `apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  kind:
    start: true
    clusterName: Kind_With_Creds
`
	if err := os.WriteFile(profilePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadIntegrationProfile(profilePath)
	if err == nil || !strings.Contains(err.Error(), "spec.kind.clusterName must be a DNS-1123 label") {
		t.Fatalf("expected invalid clusterName error, got %v", err)
	}
}

func TestLoadIntegrationProfileAcceptsSupportedPlaceholders(t *testing.T) {
	dir := t.TempDir()
	kindConfigPath := filepath.Join(dir, "kind.yaml")
	if err := os.WriteFile(kindConfigPath, []byte("kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(dir, "profile.yaml")
	content := `apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  kind:
    start: true
    config: kind.yaml
    containers:
      - ${probeImage}
    commands:
      - command: docker build -f ${repoRoot}/examples/integration/probe/Dockerfile -t ${probeImage} ${repoRoot} && test -d ${integrationProfileDir}
        timeout: 300
  setup:
    commands:
      - command: kubectl --context ${kubeContext} -n ${namespace} apply -f ${workspaceDir}/fixtures.yaml && kind load docker-image ${probeImage} --name ${kindCluster}
        timeout: 300
`
	if err := os.WriteFile(profilePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	profile, err := LoadIntegrationProfile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(profile.Spec.KIND.Config) {
		t.Fatalf("kind config path was not resolved to absolute path: %s", profile.Spec.KIND.Config)
	}
}

func TestLoadIntegrationProfileAcceptsHelmApps(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.yaml")
	content := `apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  helmApps:
    - name: my-service
      chart: my-service
      repo: https://charts.example.invalid
      namespace: application
      values:
        - ${repoRoot}/integration/values/my-service.yaml
        - ${integrationProfileDir}/values/my-service.yaml
      set:
        image.tag: "1.2.3"
      wait: true
      timeout: 300s
`
	if err := os.WriteFile(profilePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	profile, err := LoadIntegrationProfile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Spec.HelmApps) != 1 {
		t.Fatalf("helm app count = %d", len(profile.Spec.HelmApps))
	}
	if profile.Spec.HelmApps[0].Repo != "https://charts.example.invalid" {
		t.Fatalf("helm app repo = %q", profile.Spec.HelmApps[0].Repo)
	}
}

func TestLoadIntegrationProfileExtendsParent(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "base.yaml")
	childPath := filepath.Join(dir, "child.yaml")
	if err := os.WriteFile(parentPath, []byte(`apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  kind:
    start: true
    clusterName: kind
    commands:
      - command: kind load docker-image ${probeImage} --name ${kindCluster}
  helmApps:
    - name: redpanda
      chart: oci://registry.example.invalid/team/redpanda
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(childPath, []byte(`apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  extends:
    - base.yaml
  setup:
    commands:
      - command: kubectl get namespace ${namespace}
  helmApps:
    - name: app
      chart: oci://registry.example.invalid/team/app
`), 0o644); err != nil {
		t.Fatal(err)
	}
	profile, err := LoadIntegrationProfile(childPath)
	if err != nil {
		t.Fatal(err)
	}
	if !profile.Spec.KIND.Start || profile.Spec.KIND.ClusterName != "kind" {
		t.Fatalf("parent kind settings were not inherited: %+v", profile.Spec.KIND)
	}
	if len(profile.Spec.KIND.Commands) != 1 || len(profile.Spec.Setup.Commands) != 1 || len(profile.Spec.HelmApps) != 2 {
		t.Fatalf("profile merge mismatch: kind commands=%d setup commands=%d helm apps=%d", len(profile.Spec.KIND.Commands), len(profile.Spec.Setup.Commands), len(profile.Spec.HelmApps))
	}
}

func TestLoadIntegrationProfileRejectsInvalidHelmApp(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.yaml")
	content := `apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  helmApps:
    - name: bad/app
      chart: ""
`
	if err := os.WriteFile(profilePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadIntegrationProfile(profilePath)
	if err == nil || !strings.Contains(err.Error(), "spec.helmApps[0].name") {
		t.Fatalf("expected helm app validation error, got %v", err)
	}
}

func TestLoadIntegrationProfileRejectsFakeServiceByDefault(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.yaml")
	content := `apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  setup:
    commands:
      - command: helm upgrade --install wiremock ./charts/wiremock --namespace ${namespace}
        timeout: 300
`
	if err := os.WriteFile(profilePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadIntegrationProfile(profilePath)
	if err == nil || !strings.Contains(err.Error(), "references a fake/mock service") {
		t.Fatalf("expected fake service rejection, got %v", err)
	}
}

func TestLoadIntegrationProfileAcceptsFakeServiceWhenExplicit(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.yaml")
	content := `apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  allowFakes: true
  setup:
    commands:
      - command: helm upgrade --install wiremock ./charts/wiremock --namespace ${namespace}
        timeout: 300
`
	if err := os.WriteFile(profilePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadIntegrationProfile(profilePath); err != nil {
		t.Fatal(err)
	}
}

func TestLoadIntegrationProfileRejectsLiteralSecretValues(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.yaml")
	content := `apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  setup:
    commands:
      - command: kubectl -n ${namespace} create secret generic graphql-probe-credentials --from-literal=token=plain-token-value
        timeout: 60
`
	if err := os.WriteFile(profilePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadIntegrationProfile(profilePath)
	if err == nil || !strings.Contains(err.Error(), "contains a literal secret value") {
		t.Fatalf("expected literal secret rejection, got %v", err)
	}
}

func TestLoadIntegrationProfileAllowsSecretEnvironmentReferences(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.yaml")
	content := `apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  setup:
    commands:
      - command: kubectl -n ${namespace} create secret generic graphql-probe-credentials --from-literal=token="$SPEX_GRAPHQL_TOKEN"
        timeout: 60
`
	if err := os.WriteFile(profilePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadIntegrationProfile(profilePath); err != nil {
		t.Fatal(err)
	}
}

func TestLoadIntegrationProfileRejectsURLUserinfo(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.yaml")
	content := `apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  setup:
    commands:
      - command: helm repo add private https://user:password@example.com/charts
        timeout: 60
`
	if err := os.WriteFile(profilePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadIntegrationProfile(profilePath)
	if err == nil || !strings.Contains(err.Error(), "URL userinfo") {
		t.Fatalf("expected URL userinfo rejection, got %v", err)
	}
}

func TestValidateIntegrationInputsRejectsMismatchedKINDContext(t *testing.T) {
	inputs := Inputs{
		KubeContext: "kind-other",
		Integration: &IntegrationProfile{
			Spec: IntegrationProfileSpec{
				KIND: KINDIntegration{
					Start:       true,
					ClusterName: "kind",
				},
			},
		},
	}

	err := ValidateIntegrationInputs(inputs)
	if err == nil || !strings.Contains(err.Error(), `requires kubeContext "kind-kind"`) {
		t.Fatalf("expected mismatched kind context error, got %v", err)
	}
}

func TestValidateIntegrationInputsAcceptsMatchingKINDContext(t *testing.T) {
	inputs := Inputs{
		KubeContext: "kind-kind",
		Integration: &IntegrationProfile{
			Spec: IntegrationProfileSpec{
				KIND: KINDIntegration{
					Start:       true,
					ClusterName: "kind",
				},
			},
		},
	}

	if err := ValidateIntegrationInputs(inputs); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRuntimeInputsRejectsProbeImageWhitespace(t *testing.T) {
	inputs := Inputs{
		Namespace: "spex-test",
		RunID:     "run-fixed-test",
		Binding: TargetBinding{
			Spec: BindingSpec{
				Probe: Probe{
					Image: "bad image",
				},
			},
		},
	}

	err := ValidateRuntimeInputs(inputs)
	if err == nil || !strings.Contains(err.Error(), "probe image must not contain whitespace") {
		t.Fatalf("expected probe image whitespace error, got %v", err)
	}
}

func TestLoadInputsRejectsIncompleteKeycloakGraphQLAuth(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `  graphql:
    endpoint: http://graphql-api.application.svc.cluster.local/graphql`, `  graphql:
    endpoint: http://graphql-api.application.svc.cluster.local/graphql
    auth:
      type: keycloakClientCredentials
      tokenURL: http://keycloak.identity.svc.cluster.local/realms/dev/protocol/openid-connect/token`, 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.graphql.auth.clientID is required") {
		t.Fatalf("expected keycloak clientID validation error, got %v", err)
	}
}

func TestLoadInputsRejectsMissingKeycloakClientSecretRef(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `  graphql:
    endpoint: http://graphql-api.application.svc.cluster.local/graphql
    credentialsRef: graphql-token`, `  graphql:
    endpoint: http://graphql-api.application.svc.cluster.local/graphql
    auth:
      type: keycloakClientCredentials
      tokenURL: http://keycloak.identity.svc.cluster.local/realms/dev/protocol/openid-connect/token
      clientID: spex`, 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.graphql.auth.clientSecretRef is required") {
		t.Fatalf("expected keycloak clientSecretRef validation error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidKeycloakClientID(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `        password: password`, `        password: password
    keycloak-client:
      type: kubernetesSecret
      name: keycloak-client-credentials
      keys:
        clientSecret: client-secret`, 1))
	content = []byte(strings.Replace(string(content), `  graphql:
    endpoint: http://graphql-api.application.svc.cluster.local/graphql
    credentialsRef: graphql-token`, `  graphql:
    endpoint: http://graphql-api.application.svc.cluster.local/graphql
    auth:
      type: keycloakClientCredentials
      tokenURL: http://keycloak.identity.svc.cluster.local/realms/dev/protocol/openid-connect/token
      clientID: "bad client"
      clientSecretRef: keycloak-client`, 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.graphql.auth.clientID must not contain whitespace") {
		t.Fatalf("expected keycloak clientID validation error, got %v", err)
	}
}

func TestLoadInputsRejectsUnsupportedKeycloakTokenURLScheme(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `        password: password`, `        password: password
    keycloak-client:
      type: kubernetesSecret
      name: keycloak-client-credentials
      keys:
        clientSecret: client-secret`, 1))
	content = []byte(strings.Replace(string(content), `  graphql:
    endpoint: http://graphql-api.application.svc.cluster.local/graphql
    credentialsRef: graphql-token`, `  graphql:
    endpoint: http://graphql-api.application.svc.cluster.local/graphql
    auth:
      type: keycloakClientCredentials
      tokenURL: ftp://keycloak.identity.svc.cluster.local/realms/dev/protocol/openid-connect/token
      clientID: spex
      clientSecretRef: keycloak-client`, 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), `spec.graphql.auth.tokenURL uses unsupported URL scheme "ftp"`) {
		t.Fatalf("expected keycloak tokenURL scheme validation error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidKeycloakScope(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `        password: password`, `        password: password
    keycloak-client:
      type: kubernetesSecret
      name: keycloak-client-credentials
      keys:
        clientSecret: client-secret`, 1))
	content = []byte(strings.Replace(string(content), `  graphql:
    endpoint: http://graphql-api.application.svc.cluster.local/graphql
    credentialsRef: graphql-token`, `  graphql:
    endpoint: http://graphql-api.application.svc.cluster.local/graphql
    auth:
      type: keycloakClientCredentials
      tokenURL: http://keycloak.identity.svc.cluster.local/realms/dev/protocol/openid-connect/token
      clientID: spex
      clientSecretRef: keycloak-client
      scopes:
        - "openid profile"`, 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.graphql.auth.scopes[0]") {
		t.Fatalf("expected keycloak scope validation error, got %v", err)
	}
}

func TestLoadInputsAcceptsKeycloakGraphQLAuth(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `        password: password`, `        password: password
    keycloak-client:
      type: kubernetesSecret
      name: keycloak-client-credentials
      keys:
        clientSecret: client-secret`, 1))
	content = []byte(strings.Replace(string(content), `  graphql:
    endpoint: http://graphql-api.application.svc.cluster.local/graphql`, `  graphql:
    endpoint: http://graphql-api.application.svc.cluster.local/graphql
    auth:
      type: keycloakClientCredentials
      tokenURL: http://keycloak.identity.svc.cluster.local/realms/dev/protocol/openid-connect/token
      clientID: spex
      clientSecretRef: keycloak-client
      scopes:
        - openid`, 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadInputs(scenarioPath, bindingPath); err != nil {
		t.Fatal(err)
	}
}

func TestLoadInputsRejectsInvalidSecretName(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "name: mqtt-probe-credentials", "name: MQTT_probe_credentials", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.secrets.mqtt-credentials.name must be a DNS-1123 subdomain") {
		t.Fatalf("expected secret name validation error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidSecretID(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "mqtt-credentials:", "mqtt/credentials:", 1))
	content = []byte(strings.Replace(string(content), "credentialsRef: mqtt-credentials", "credentialsRef: mqtt/credentials", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), `spec.secrets contains invalid id "mqtt/credentials"`) {
		t.Fatalf("expected secret id validation error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidSecretKeyMapping(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "password: password", "password: pass/word", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.secrets.mqtt-credentials.keys.password") {
		t.Fatalf("expected secret key mapping validation error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidSecretLogicalKey(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "password: password", "bad-key: password", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), `contains invalid logical key "bad-key"`) {
		t.Fatalf("expected secret logical key validation error, got %v", err)
	}
}

func TestLoadInputsRejectsEmptySecretKeys(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `    mqtt-credentials:
      type: kubernetesSecret
      name: mqtt-probe-credentials
      keys:
        username: username
        password: password`, `    mqtt-credentials:
      type: kubernetesSecret
      name: mqtt-probe-credentials
      keys: {}`, 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.secrets.mqtt-credentials.keys must contain at least one key mapping") {
		t.Fatalf("expected empty secret keys validation error, got %v", err)
	}
}

func TestLoadInputsRejectsUnknownRedpandaTopicRef(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "topicRef: normalized-readings", "topicRef: missing-topic", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "unknown redpanda topicRef") {
		t.Fatalf("expected unknown topicRef error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidRedpandaTopicRefID(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	scenarioContent, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	scenarioContent = []byte(strings.Replace(string(scenarioContent), "topicRef: normalized-readings", "topicRef: normalized/readings", 1))
	if err := os.WriteFile(scenarioPath, scenarioContent, 0o644); err != nil {
		t.Fatal(err)
	}
	bindingContent, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	bindingContent = []byte(strings.Replace(string(bindingContent), "normalized-readings:", "normalized/readings:", 1))
	if err := os.WriteFile(bindingPath, bindingContent, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), `invalid topic ref "normalized/readings"`) {
		t.Fatalf("expected invalid topic ref validation error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidRedpandaTopicName(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "name: ingestion.normalized-readings", "name: ingestion/normalized-readings", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "must be a valid Kafka topic name") {
		t.Fatalf("expected invalid Kafka topic name validation error, got %v", err)
	}
}

func TestLoadInputsRejectsRedpandaTopicWithoutOffsetSnapshot(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "allowOffsetSnapshot: true", "allowOffsetSnapshot: false", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "allowOffsetSnapshot: true") {
		t.Fatalf("expected allowOffsetSnapshot error, got %v", err)
	}
}

func TestLoadInputsRejectsCompactedRedpandaTopic(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "allowOffsetSnapshot: true", "allowOffsetSnapshot: true\n        allowCompacted: true", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "compacted topics") {
		t.Fatalf("expected compacted topic error, got %v", err)
	}
}

func TestLoadInputsRejectsGraphQLVariablesOnlyInComments(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	writeQuery(t, dir, `query LatestDeviceReading {
  # $scenarioRunId $correlationId
  latestDeviceReading {
    value
  }
}`)

	_, err := LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "graphql_query_contract_failure") {
		t.Fatalf("expected graphql query contract error, got %v", err)
	}
}

func TestLoadInputsRejectsGraphQLVariablesOnlyInSignature(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	writeQuery(t, dir, `query LatestDeviceReading($scenarioRunId: String!, $correlationId: String!) {
  latestDeviceReading {
    value
  }
}`)

	_, err := LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "query must use $scenarioRunId") {
		t.Fatalf("expected missing executable variable error, got %v", err)
	}
}

func TestLoadInputsRejectsDuplicateGraphQLQueryConfigMapKey(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `    latest-device-reading:
      file: examples/queries/latest-device-reading.graphql`, `    latest-device-reading:
      file: examples/queries/latest-device-reading.graphql
    duplicate-latest-device-reading:
      file: other/latest-device-reading.graphql`, 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), `same generated ConfigMap key "latest-device-reading.graphql"`) {
		t.Fatalf("expected duplicate GraphQL query key error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidGraphQLQueryRef(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "latest-device-reading:", "latest/device-reading:", 1))
	content = []byte(strings.Replace(string(content), "queryRef: latest-device-reading", "queryRef: latest/device-reading", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), `spec.graphqlQueries contains invalid ref "latest/device-reading"`) {
		t.Fatalf("expected invalid GraphQL query ref error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidGraphQLQueryConfigMapKey(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "examples/queries/latest-device-reading.graphql", "examples/queries/latest:reading.graphql", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "must be a valid ConfigMap or Secret data key") {
		t.Fatalf("expected invalid GraphQL query ConfigMap key error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidDuration(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "timeout: 45s", "timeout: soon", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.defaults.timeout must be a Go duration") {
		t.Fatalf("expected duration validation error, got %v", err)
	}
}

func TestLoadInputsRejectsSubsecondTimeout(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "timeout: 45s", "timeout: 500ms", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "spec.defaults.timeout must be at least 1s") {
		t.Fatalf("expected subsecond timeout validation error, got %v", err)
	}
}

func TestLoadInputsAcceptsSubsecondPollInterval(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")

	if _, err := LoadInputs(scenarioPath, bindingPath); err != nil {
		t.Fatal(err)
	}
}

func TestLoadInputsRejectsForwardAfterDependency(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "after: publish-reading-1", "after: assert-reading-1-in-graphql", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "dependencies must appear earlier") {
		t.Fatalf("expected forward dependency error, got %v", err)
	}
}

func TestLoadInputsRejectsDuplicateOperationID(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "id: assert-reading-1-in-redpanda", "id: publish-reading-1", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), `duplicate operation id "publish-reading-1"`) {
		t.Fatalf("expected duplicate operation id error, got %v", err)
	}
}

func TestLoadInputsRejectsUnknownTemplateParameter(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "${param.deviceId}", "${param.devcieId}", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), `unknown parameter "devcieId"`) {
		t.Fatalf("expected unknown parameter error, got %v", err)
	}
}

func TestLoadInputsRejectsEmbeddedJSONPayloadPlaceholder(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `"deviceId":"${param.deviceId}"`, `"deviceId":"dev-${param.deviceId}"`, 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "template placeholder embedded inside a larger JSON string value") {
		t.Fatalf("expected embedded JSON placeholder error, got %v", err)
	}
}

func TestLoadInputsRejectsWeakRedpandaCorrelation(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `          - path: $.scenarioRunId
            equalsString: ${scenarioRunId}
`, "", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "redpanda.match must include an equalsString matcher for ${scenarioRunId}") {
		t.Fatalf("expected weak redpanda correlation error, got %v", err)
	}
}

func TestLoadInputsRejectsWeakGraphQLCorrelation(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `          scenarioRunId: ${scenarioRunId}
`, "", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "graphql.variables.scenarioRunId must be ${scenarioRunId}") {
		t.Fatalf("expected weak graphql correlation error, got %v", err)
	}
}

func TestLoadInputsRejectsTemplateCorrelationID(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "        correlationId: reading-1\n", "        correlationId: ${scenarioRunId}\n", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "mqtt.correlationId must not contain template expressions") {
		t.Fatalf("expected template correlationId error, got %v", err)
	}
}

func TestLoadInputsRejectsControlCharacterCorrelationID(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "        correlationId: reading-1\n", "        correlationId: \"reading\\t1\"\n", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "mqtt.correlationId must not contain control characters") {
		t.Fatalf("expected control character correlationId error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidGraphQLVariableName(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `          deviceId: ${param.deviceId}`, `          device-id: ${param.deviceId}`, 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), `invalid GraphQL variable name "device-id"`) {
		t.Fatalf("expected invalid GraphQL variable name error, got %v", err)
	}
}

func TestLoadInputsRejectsUnsupportedTemplateReference(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "equalsString: reading-1", "equalsString: ${env.DEVICE_ID}", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), `unsupported template reference "env.DEVICE_ID"`) {
		t.Fatalf("expected unsupported template reference error, got %v", err)
	}
}

func TestLoadInputsRejectsUnknownScenarioParameterOverride(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "deviceId: device-dev-1", "devcieId: device-dev-1", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), `unknown parameter "devcieId"`) {
		t.Fatalf("expected unknown scenario parameter override error, got %v", err)
	}
}

func TestLoadInputsRejectsMissingRequiredScenarioParameter(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	scenarioContent, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	scenarioContent = []byte(strings.Replace(string(scenarioContent), "      default: device-dev-1\n", "", 1))
	if err := os.WriteFile(scenarioPath, scenarioContent, 0o644); err != nil {
		t.Fatal(err)
	}
	bindingContent, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	bindingContent = []byte(strings.Replace(string(bindingContent), "    deviceId: device-dev-1\n", "", 1))
	if err := os.WriteFile(bindingPath, bindingContent, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), `required parameter "deviceId"`) {
		t.Fatalf("expected missing required parameter error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidScenarioParameterName(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "    deviceId:", "    device-id:", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), `invalid name`) {
		t.Fatalf("expected invalid parameter name error, got %v", err)
	}
}

func TestLoadInputsRejectsScenarioParameterTemplateValue(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "deviceId: device-dev-1", "deviceId: ${env.DEVICE_ID}", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "must not contain template expressions") {
		t.Fatalf("expected parameter template value error, got %v", err)
	}
}

func TestLoadInputsRejectsMQTTTopicParameterRestrictionViolation(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "deviceId: device-dev-1", "deviceId: device/dev-1", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "violates MQTT topic restrictions") {
		t.Fatalf("expected MQTT topic parameter restriction error, got %v", err)
	}
}

func TestLoadInputsRejectsMQTTTopicWildcard(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "telemetry/${param.deviceId}/readings", "telemetry/${param.deviceId}/#", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "must not contain MQTT wildcard") {
		t.Fatalf("expected MQTT wildcard topic error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidPayloadTemplateRef(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "valid-energy-reading:", "valid/energy-reading:", 1))
	content = []byte(strings.Replace(string(content), "payloadTemplateRef: valid-energy-reading", "payloadTemplateRef: valid/energy-reading", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), `spec.payloadTemplates contains invalid ref "valid/energy-reading"`) {
		t.Fatalf("expected invalid payload template ref error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidMatcherShape(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `          - path: $.correlationId
            equalsString: reading-1`, `          - path: $.correlationId
            equalsString: reading-1
            equalsNumber: "1"`, 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "must specify exactly one expectation") {
		t.Fatalf("expected invalid matcher shape error, got %v", err)
	}
}

func TestLoadInputsRejectsInvalidMatcherPath(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "path: $.correlationId", "path: $.items[*]", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "path has unsupported syntax") {
		t.Fatalf("expected invalid matcher path error, got %v", err)
	}
}

func TestLoadInputsRejectsNonJSONPayloadTemplateContentType(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "contentType: application/json", "contentType: text/plain", 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "contentType must be application/json") {
		t.Fatalf("expected payload contentType error, got %v", err)
	}
}

func TestLoadInputsRejectsMissingMQTTOperationFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		old     string
		new     string
		wantErr string
	}{
		{
			name:    "topic",
			old:     "        topic: telemetry/${param.deviceId}/readings\n",
			wantErr: "mqtt.topic is required",
		},
		{
			name:    "payload template ref",
			old:     "        payloadTemplateRef: valid-energy-reading\n",
			wantErr: "mqtt.payloadTemplateRef is required",
		},
		{
			name:    "correlation id",
			old:     "        correlationId: reading-1\n",
			wantErr: "mqtt.correlationId is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
			content, err := os.ReadFile(scenarioPath)
			if err != nil {
				t.Fatal(err)
			}
			content = []byte(strings.Replace(string(content), tc.old, tc.new, 1))
			if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
				t.Fatal(err)
			}

			_, err = LoadInputs(scenarioPath, bindingPath)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q error, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoadInputsRejectsMQTTPayloadWithoutCorrelationMarkers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		old     string
		wantErr string
	}{
		{
			name:    "scenario run id",
			old:     `"scenarioRunId":"${scenarioRunId}",`,
			wantErr: "must include ${scenarioRunId}",
		},
		{
			name:    "correlation id",
			old:     `"correlationId":"${correlationId}",`,
			wantErr: "must include ${correlationId}",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
			content, err := os.ReadFile(scenarioPath)
			if err != nil {
				t.Fatal(err)
			}
			content = []byte(strings.Replace(string(content), tc.old, "", 1))
			if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
				t.Fatal(err)
			}

			_, err = LoadInputs(scenarioPath, bindingPath)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q error, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoadInputsRejectsMixedOperationBlocks(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `        correlationId: reading-1
    - id: assert-reading-1-in-redpanda`, `        correlationId: reading-1
      redpanda:
        topicRef: normalized-readings
        correlationId: reading-1
        match:
          - path: $.scenarioRunId
            equalsString: ${scenarioRunId}
          - path: $.correlationId
            equalsString: reading-1
    - id: assert-reading-1-in-redpanda`, 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = LoadInputs(scenarioPath, bindingPath)
	if err == nil || !strings.Contains(err.Error(), "must not contain redpanda block") {
		t.Fatalf("expected mixed operation block error, got %v", err)
	}
}

func TestLoadInputsRejectsMissingAssertionOperationRefs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		old     string
		wantErr string
	}{
		{
			name:    "redpanda topic ref",
			old:     "        topicRef: normalized-readings\n",
			wantErr: "redpanda.topicRef is required",
		},
		{
			name:    "graphql query ref",
			old:     "        queryRef: latest-device-reading\n",
			wantErr: "graphql.queryRef is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
			content, err := os.ReadFile(scenarioPath)
			if err != nil {
				t.Fatal(err)
			}
			content = []byte(strings.Replace(string(content), tc.old, "", 1))
			if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
				t.Fatal(err)
			}

			_, err = LoadInputs(scenarioPath, bindingPath)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q error, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoadInputsAcceptsExampleShape(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, dir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")

	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	if inputs.ScenarioName != "mqtt-ingestion-basic" {
		t.Fatalf("ScenarioName = %q", inputs.ScenarioName)
	}
	if len(inputs.Scenario.Spec.Operations) != 3 {
		t.Fatalf("operation count = %d", len(inputs.Scenario.Spec.Operations))
	}
}

func TestLoadFeatureScenariosSupportsBackgroundAndMultipleScenarios(t *testing.T) {
	dir := t.TempDir()
	featurePath := filepath.Join(dir, "readings.feature")
	if err := os.WriteFile(featurePath, []byte(`Feature: MQTT ingestion

  Background:
    Given tenant "tenant-dev"
    And device "device-dev-1"

  @redpanda
  Scenario: first reading
    When device "device-dev-1" publishes energy reading 42.5 as "reading-1"
    Then Redpanda contains reading "reading-1" with value 42.5

  Scenario: second reading
    When device "device-dev-1" publishes energy reading 11.0 as "reading-2"
    Then Redpanda contains reading "reading-2" with value 11.0
`), 0o644); err != nil {
		t.Fatal(err)
	}

	scenarios, err := loadFeatureScenarios(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(scenarios) != 2 {
		t.Fatalf("scenario count = %d", len(scenarios))
	}
	if scenarios[0].Metadata.Name != "first-reading" || scenarios[1].Metadata.Name != "second-reading" {
		t.Fatalf("unexpected scenario names: %q, %q", scenarios[0].Metadata.Name, scenarios[1].Metadata.Name)
	}
	if strings.Join(scenarios[0].Metadata.Tags, ",") != "redpanda" {
		t.Fatalf("first scenario tags = %#v", scenarios[0].Metadata.Tags)
	}
	for _, scenario := range scenarios {
		if got := len(scenario.Spec.StepInvocations); got != 4 {
			t.Fatalf("%s step count = %d", scenario.Metadata.Name, got)
		}
		if scenario.Spec.StepInvocations[0] != (StepInvocation{Kind: "given", Text: `tenant "tenant-dev"`}) {
			t.Fatalf("background tenant step not prepended: %#v", scenario.Spec.StepInvocations[0])
		}
		if scenario.Spec.StepInvocations[1] != (StepInvocation{Kind: "given", Text: `device "device-dev-1"`}) {
			t.Fatalf("background device step not prepended with And kind resolved: %#v", scenario.Spec.StepInvocations[1])
		}
	}
}

func TestLoadFeatureScenariosRejectsDuplicateScenarioSlugs(t *testing.T) {
	dir := t.TempDir()
	featurePath := filepath.Join(dir, "readings.feature")
	if err := os.WriteFile(featurePath, []byte(`Feature: MQTT ingestion

  Scenario: reading one
    Given tenant "tenant-dev"

  Scenario: reading_one
    Given tenant "tenant-dev"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := loadFeatureScenarios(featurePath)
	if err == nil || !strings.Contains(err.Error(), `duplicate Scenario slug "reading-one"`) {
		t.Fatalf("expected duplicate scenario slug error, got %v", err)
	}
}

func TestLoadFeatureScenariosSupportsScenarioOutline(t *testing.T) {
	dir := t.TempDir()
	featurePath := filepath.Join(dir, "readings.feature")
	if err := os.WriteFile(featurePath, []byte(`@smoke
Feature: MQTT ingestion

  Background:
    Given tenant "tenant-dev"

  @graphql
  Scenario Outline: reading <correlationId>
    When device "device-dev-1" publishes energy reading <value> as "<correlationId>"
    Then Redpanda contains reading "<correlationId>" with value <value>

  Examples:
    | correlationId | value |
    | reading-1     | 42.5  |
    | reading-2     | 11.0  |
`), 0o644); err != nil {
		t.Fatal(err)
	}

	scenarios, err := loadFeatureScenarios(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(scenarios) != 2 {
		t.Fatalf("scenario count = %d", len(scenarios))
	}
	if scenarios[0].Metadata.Name != "reading-reading-1-1" || scenarios[1].Metadata.Name != "reading-reading-2-2" {
		t.Fatalf("unexpected scenario names: %q, %q", scenarios[0].Metadata.Name, scenarios[1].Metadata.Name)
	}
	if strings.Join(scenarios[0].Metadata.Tags, ",") != "smoke,graphql" {
		t.Fatalf("outline scenario tags = %#v", scenarios[0].Metadata.Tags)
	}
	if scenarios[0].Spec.StepInvocations[1].Text != `device "device-dev-1" publishes energy reading 42.5 as "reading-1"` {
		t.Fatalf("outline placeholders were not rendered: %#v", scenarios[0].Spec.StepInvocations[1])
	}
}

func TestLoadCatalogBundleRejectsEmptyFlowCatalog(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "flow-catalog.yaml")
	if err := os.WriteFile(catalogPath, []byte(`apiVersion: spex.catalog.v0.1
kind: FlowCatalog
metadata:
  name: empty-flow-catalog
spec:
  flows: {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadCatalogBundle([]string{catalogPath})
	if err == nil || !strings.Contains(err.Error(), "spec.flows must contain at least one flow") {
		t.Fatalf("expected empty flow catalog validation error, got %v", err)
	}
}

func TestLoadCatalogBundleRejectsFlowWithoutOperations(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "flow-catalog.yaml")
	if err := os.WriteFile(catalogPath, []byte(`apiVersion: spex.catalog.v0.1
kind: FlowCatalog
metadata:
  name: flow-catalog
spec:
  flows:
    noOps:
      expandsTo:
        operations: []
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadCatalogBundle([]string{catalogPath})
	if err == nil || !strings.Contains(err.Error(), "spec.flows.noOps.expandsTo.operations must contain at least one operation") {
		t.Fatalf("expected empty flow operation validation error, got %v", err)
	}
}

func TestLoadCatalogBundleRejectsEmptyStepCatalog(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "step-catalog.yaml")
	if err := os.WriteFile(catalogPath, []byte(`apiVersion: spex.catalog.v0.1
kind: StepCatalog
metadata:
  name: empty-step-catalog
spec:
  steps: []
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadCatalogBundle([]string{catalogPath})
	if err == nil || !strings.Contains(err.Error(), "spec.steps must contain at least one step") {
		t.Fatalf("expected empty step catalog validation error, got %v", err)
	}
}

func TestLoadCatalogBundleRejectsEmptyStepOutput(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "step-catalog.yaml")
	if err := os.WriteFile(catalogPath, []byte(`apiVersion: spex.catalog.v0.1
kind: StepCatalog
metadata:
  name: step-catalog
spec:
  steps:
    - kind: given
      expression: tenant "{tenantId}"
      output: {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadCatalogBundle([]string{catalogPath})
	if err == nil || !strings.Contains(err.Error(), "spec.steps[0].output must contain parameters, payloadTemplates, graphqlQueries, or operations") {
		t.Fatalf("expected empty step output validation error, got %v", err)
	}
}

func TestLoadCatalogBundleAcceptsParameterOnlyStepOutput(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "step-catalog.yaml")
	if err := os.WriteFile(catalogPath, []byte(`apiVersion: spex.catalog.v0.1
kind: StepCatalog
metadata:
  name: step-catalog
spec:
  steps:
    - kind: given
      expression: tenant "{tenantId}"
      output:
        parameters:
          tenantId:
            type: string
            default: "{tenantId}"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadCatalogBundle([]string{catalogPath}); err != nil {
		t.Fatalf("expected parameter-only step output to validate: %v", err)
	}
}

func writeScenarioAndBinding(t *testing.T, dir, secretType, brokerURL string) (string, string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	scenarioPath := filepath.Join(dir, "scenario.yaml")
	bindingPath := filepath.Join(dir, "binding.yaml")
	if err := os.WriteFile(scenarioPath, []byte(minimalScenario()), 0o644); err != nil {
		t.Fatal(err)
	}
	writeQuery(t, dir, `query LatestDeviceReading($scenarioRunId: String!, $correlationId: String!, $deviceId: String!) {
  latestDeviceReading(scenarioRunId: $scenarioRunId, correlationId: $correlationId, deviceId: $deviceId) {
    value
  }
}`)
	if err := os.WriteFile(bindingPath, []byte(minimalBinding(secretType, brokerURL)), 0o644); err != nil {
		t.Fatal(err)
	}
	return scenarioPath, bindingPath
}

func writeQuery(t *testing.T, dir, content string) {
	t.Helper()
	queryDir := filepath.Join(dir, "examples", "queries")
	if err := os.MkdirAll(queryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queryDir, "latest-device-reading.graphql"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGitForTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(output))
	}
}

func TestLimitedCommandCaptureTruncatesWithoutShortWrite(t *testing.T) {
	capture := newLimitedCommandCapture(5)
	n, err := capture.Write([]byte("abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Fatalf("expected full write acknowledgement, got %d", n)
	}
	got := capture.String()
	if !strings.HasPrefix(got, "abcde\n[spex: command output truncated after 5 bytes]") {
		t.Fatalf("unexpected captured output: %q", got)
	}
}

func minimalScenario() string {
	return `apiVersion: spex.scenario.v0.1
kind: Scenario
metadata:
  name: mqtt-ingestion-basic
spec:
  defaults:
    timeout: 45s
    pollInterval: 250ms
  parameters:
    deviceId:
      type: string
      default: device-dev-1
  payloadTemplates:
    valid-energy-reading:
      contentType: application/json
      body: |
        {"scenarioRunId":"${scenarioRunId}","correlationId":"${correlationId}","deviceId":"${param.deviceId}"}
  graphqlQueries:
    latest-device-reading:
      file: examples/queries/latest-device-reading.graphql
  operations:
    - id: publish-reading-1
      type: mqtt.publish
      mqtt:
        topic: telemetry/${param.deviceId}/readings
        payloadTemplateRef: valid-energy-reading
        correlationId: reading-1
    - id: assert-reading-1-in-redpanda
      type: redpanda.contains
      after: publish-reading-1
      redpanda:
        topicRef: normalized-readings
        correlationId: reading-1
        match:
          - path: $.scenarioRunId
            equalsString: ${scenarioRunId}
          - path: $.correlationId
            equalsString: reading-1
    - id: assert-reading-1-in-graphql
      type: graphql.expect
      after: publish-reading-1
      graphql:
        queryRef: latest-device-reading
        variables:
          scenarioRunId: ${scenarioRunId}
          correlationId: reading-1
          deviceId: ${param.deviceId}
        match:
          - path: $.data.latestDeviceReading.scenarioRunId
            equalsString: ${scenarioRunId}
          - path: $.data.latestDeviceReading.correlationId
            equalsString: reading-1
`
}

func minimalBinding(secretType, brokerURL string) string {
	return `apiVersion: spex.binding.v0.1
kind: TargetBinding
metadata:
  name: local-dev
spec:
  kubeContext: local-dev
  namespace: spex-test
  scenarioParameters:
    deviceId: device-dev-1
  rbac:
    create: true
  probe:
    image: spex-probe:dev
    imagePullPolicy: IfNotPresent
    serviceAccountName: spex-probe
  secrets:
    mqtt-credentials:
      type: ` + secretType + `
      name: mqtt-probe-credentials
      keys:
        username: username
        password: password
    graphql-token:
      type: kubernetesSecret
      name: graphql-probe-credentials
      keys:
        token: token
  mqtt:
    brokerURL: ` + brokerURL + `
    credentialsRef: mqtt-credentials
  redpanda:
    brokers: redpanda.streaming.svc.cluster.local:9092
    topics:
      normalized-readings:
        name: ingestion.normalized-readings
        allowOffsetSnapshot: true
  graphql:
    endpoint: http://graphql-api.application.svc.cluster.local/graphql
    credentialsRef: graphql-token
`
}
