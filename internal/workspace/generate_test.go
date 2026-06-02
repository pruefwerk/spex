package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenerateWorkspace(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, filepath.Join(dir, "inputs"), "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs.RunID = "run-fixed-test"
	out := filepath.Join(dir, "out")
	if err := Generate(out, inputs); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"kuttl-test.yaml",
		"step-map.yaml",
		filepath.Join("kuttl", "mqtt-ingestion-basic", "01-rbac.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "01-static-configmaps.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "02-op-redpanda-snapshot-offsets.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "02-assert.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "03-op-publish-reading-1.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "03-assert.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "04-op-assert-reading-1-in-redpanda.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "04-assert.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "05-op-assert-reading-1-in-graphql.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "05-assert.yaml"),
		filepath.Join("rendered", "payloads", "publish-reading-1.json"),
		filepath.Join("rendered", "variables", "assert-reading-1-in-graphql.variables.json"),
		filepath.Join("rendered", "matchers", "assert-reading-1-in-redpanda.matchers.json"),
	} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
	staticConfigMaps, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "01-static-configmaps.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`name: "spex-mqtt-ingestion-basic-payloads"`,
		`name: "spex-mqtt-ingestion-basic-graphql"`,
		`name: "spex-mqtt-ingestion-basic-variables"`,
		`name: "spex-mqtt-ingestion-basic-matchers"`,
		"publish-reading-1.json",
		"latest-device-reading.graphql",
		"assert-reading-1-in-graphql.variables.json",
		"assert-reading-1-in-redpanda.matchers.json",
		"assert-reading-1-in-graphql.matchers.json",
	} {
		if !strings.Contains(string(staticConfigMaps), want) {
			t.Fatalf("static ConfigMaps missing %q", want)
		}
	}
	stepMap, err := os.ReadFile(filepath.Join(out, "step-map.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stepMap), `kubeContext: "local-dev"`) {
		t.Fatalf("step map missing kubeContext:\n%s", string(stepMap))
	}
	for _, want := range []string{
		`scenarioFile: "` + filepath.ToSlash(scenarioPath) + `"`,
		`bindingFile: "` + filepath.ToSlash(bindingPath) + `"`,
		`spex/operation-id: "publish-reading-1"`,
		`spex/step-ordinal: "03"`,
	} {
		if !strings.Contains(string(stepMap), want) {
			t.Fatalf("step map missing %q:\n%s", want, string(stepMap))
		}
	}
	mqttJob, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "03-op-publish-reading-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"SPEX_MQTT_USERNAME",
		"secretKeyRef:",
		`name: "mqtt-probe-credentials"`,
		`key: "username"`,
		`key: "password"`,
		`"--client-id=spex-mqtt-ingestion-basic-run-fixed-test-03-publish-reading-1"`,
		`"--qos=1"`,
		`"--timeout=45s"`,
		`spex/operation-type: "mqtt.publish"`,
		`spex/step-ordinal: "03"`,
		"readOnly: true",
		"activeDeadlineSeconds: 75",
	} {
		if !strings.Contains(string(mqttJob), want) {
			t.Fatalf("MQTT Job missing %q", want)
		}
	}
	snapshotJob, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "02-op-redpanda-snapshot-offsets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"--offsets-configmap=spex-mqtt-ingestion-basic-redpanda-offsets"`,
		`"--namespace=spex-test"`,
		`"--scenario=mqtt-ingestion-basic"`,
		`"--run-id=run-fixed-test"`,
		`"--timeout=45s"`,
		`"--topic=ingestion.normalized-readings"`,
		"activeDeadlineSeconds: 75",
	} {
		if !strings.Contains(string(snapshotJob), want) {
			t.Fatalf("Redpanda snapshot Job missing %q", want)
		}
	}
	redpandaJob, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "04-op-assert-reading-1-in-redpanda.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"--topic=ingestion.normalized-readings"`,
		`"--offsets-configmap=spex-mqtt-ingestion-basic-redpanda-offsets"`,
		`"--namespace=spex-test"`,
		`"--scenario=mqtt-ingestion-basic"`,
		`"--run-id=run-fixed-test"`,
		`"--matchers-file=/spex/matchers/assert-reading-1-in-redpanda.matchers.json"`,
		`"--timeout=45s"`,
		`"--poll-interval=250ms"`,
		"activeDeadlineSeconds: 75",
	} {
		if !strings.Contains(string(redpandaJob), want) {
			t.Fatalf("Redpanda contains Job missing %q", want)
		}
	}
	graphQLJob, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "05-op-assert-reading-1-in-graphql.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"--query-file=/spex/graphql/latest-device-reading.graphql"`,
		`"--variables-file=/spex/variables/assert-reading-1-in-graphql.variables.json"`,
		`name: "spex-mqtt-ingestion-basic-variables"`,
		`mountPath: /spex/variables`,
		`"--timeout=45s"`,
		`"--poll-interval=250ms"`,
		"activeDeadlineSeconds: 75",
	} {
		if !strings.Contains(string(graphQLJob), want) {
			t.Fatalf("GraphQL Job missing %q", want)
		}
	}
}

func TestGenerateWorkspaceMaterializesLocalEnvFileSecret(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, filepath.Join(dir, "inputs"), "localEnvFile", "tcp://emqx.platform.svc.cluster.local:1883")
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
	inputs.RunID = "run-fixed-test"
	out := filepath.Join(dir, "out")
	if err := Generate(out, inputs); err != nil {
		t.Fatal(err)
	}
	setup, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "01-integration-setup.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		". " + shellQuote(filepath.ToSlash(filepath.Join(filepath.Dir(bindingPath), "local.env"))),
		"kubectl '--context' 'local-dev' '-n' 'spex-test' 'create' 'secret' 'generic' 'mqtt-probe-credentials'",
		"--from-literal='username'=\"${SPEX_MQTT_USERNAME}\"",
		"--from-literal='password'=\"${SPEX_MQTT_PASSWORD}\"",
		"| kubectl 'label' '--local' '-f' '-' '-o' 'yaml' 'spex/owned=true' 'spex/secret-id=mqtt-credentials' 'spex/run-id=run-fixed-test'",
		"| kubectl '--context' 'local-dev' 'create' '-f' '-'",
	} {
		if !strings.Contains(string(setup), want) {
			t.Fatalf("secret materialization step missing %q:\n%s", want, string(setup))
		}
	}
	if _, err := os.Stat(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "02-static-configmaps.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateWorkspaceMaterializesSSMMQTTBrokerURL(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, filepath.Join(dir, "inputs"), "kubernetesSecret", `'{{ ssm "/dev/emqx/emqx_endpoint" }}'`)
	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs.RunID = "run-fixed-test"
	out := filepath.Join(dir, "out")
	if err := Generate(out, inputs); err != nil {
		t.Fatal(err)
	}
	setup, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "01-integration-setup.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"aws ssm get-parameter --with-decryption --name '/dev/emqx/emqx_endpoint'",
		"kubectl '--context' 'local-dev' '-n' 'spex-test' 'create' 'secret' 'generic' 'mqtt-probe-credentials-broker-url'",
		"--from-literal='brokerURL'=\"${SPEX_SSM_MQTT_BROKER_URL}\"",
		"'spex/secret-id=mqtt-broker-url'",
		"'spex/source=aws-ssm'",
	} {
		if !strings.Contains(string(setup), want) {
			t.Fatalf("broker URL materialization step missing %q:\n%s", want, string(setup))
		}
	}
	job, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "04-op-publish-reading-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`name: "SPEX_MQTT_BROKER_URL"`,
		`name: "mqtt-probe-credentials-broker-url"`,
		`key: "brokerURL"`,
	} {
		if !strings.Contains(string(job), want) {
			t.Fatalf("broker URL env missing %q:\n%s", want, string(job))
		}
	}
	if strings.Contains(string(job), "--broker-url=") {
		t.Fatalf("job should not render an SSM broker URL as a literal arg:\n%s", string(job))
	}
}

func TestGenerateEscapesRenderedJSONPayloadValues(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, filepath.Join(dir, "inputs"), "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	bindingContent, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	bindingContent = []byte(strings.Replace(string(bindingContent), "deviceId: device-dev-1", `deviceId: 'device "dev" 1'`, 1))
	if err := os.WriteFile(bindingPath, bindingContent, 0o644); err != nil {
		t.Fatal(err)
	}
	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs.RunID = "run-fixed-test"
	out := filepath.Join(dir, "out")
	if err := Generate(out, inputs); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(out, "rendered", "payloads", "publish-reading-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"scenarioRunId": "run-fixed-test"`,
		`"correlationId": "reading-1"`,
		`"deviceId": "device \"dev\" 1"`,
	} {
		if !strings.Contains(string(payload), want) {
			t.Fatalf("payload missing %q:\n%s", want, string(payload))
		}
	}
}

func TestGenerateRejectsSymlinkGeneratedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, filepath.Join(dir, "inputs"), "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs.RunID = "run-fixed-test"
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	realReadme := filepath.Join(t.TempDir(), "README.generated.md")
	if err := os.WriteFile(realReadme, []byte("real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realReadme, filepath.Join(out, "README.generated.md")); err != nil {
		t.Fatal(err)
	}

	err = Generate(out, inputs)
	if err == nil {
		t.Fatal("expected Generate to fail")
	}
	if !strings.Contains(err.Error(), "README.generated.md: not a regular file") {
		t.Fatalf("unexpected error: %v", err)
	}
	content, readErr := os.ReadFile(realReadme)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "real\n" {
		t.Fatalf("symlink target was modified: %q", string(content))
	}
}

func TestGenerateRejectsSymlinkGeneratedDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses symlinks")
	}
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, filepath.Join(dir, "inputs"), "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs.RunID = "run-fixed-test"
	out := filepath.Join(dir, "out")
	targetDir := t.TempDir()
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetDir, filepath.Join(out, "rendered")); err != nil {
		t.Fatal(err)
	}

	err = Generate(out, inputs)
	if err == nil {
		t.Fatal("expected Generate to fail")
	}
	if !strings.Contains(err.Error(), "refusing symlink directory") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(targetDir, "payloads")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected symlink target dir to remain untouched, got stat err %v", statErr)
	}
}

func TestGenerateWorkspaceWithKUTTLNativeIntegrationProfile(t *testing.T) {
	dir := t.TempDir()
	inputDir := filepath.Join(dir, "inputs")
	scenarioPath, bindingPath := writeScenarioAndBinding(t, inputDir, "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	kindConfigPath := filepath.Join(inputDir, "kind.yaml")
	if err := os.WriteFile(kindConfigPath, []byte("kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(inputDir, "profile.yaml")
	profile := `apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  kind:
    start: true
    clusterName: kind
    config: kind.yaml
    nodeCache: false
    containers:
      - ${probeImage}
    commands:
      - command: docker build -f ${repoRoot}/examples/integration/probe/Dockerfile -t ${probeImage} ${repoRoot}
        timeout: 300
      - command: kind load docker-image ${probeImage} --name ${kindCluster}
        timeout: 300
  setup:
    commands:
      - command: kubectl ${kubeContextArgs} create namespace spex-test --dry-run=client -o yaml && test -d ${integrationProfileDir}
        timeout: 60
  helmApps:
    - name: redpanda
      chart: redpanda
      repo: https://charts.redpanda.com
      namespace: spex-test
      wait: true
      timeout: 300s
`
	if err := os.WriteFile(profilePath, []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	integration, err := LoadIntegrationProfile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	inputs.Integration = &integration
	inputs.IntegrationProfilePath = profilePath
	inputs.RunID = "run-fixed-test"
	out := filepath.Join(dir, "out")
	if err := Generate(out, inputs); err != nil {
		t.Fatal(err)
	}

	kuttlTest, err := os.ReadFile(filepath.Join(out, "kuttl-test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"startKIND: true",
		"kindConfig: ./kind.yaml",
		"kindNodeCache: false",
		"artifactsDir: artifacts/kuttl",
		"reportFormat: xml",
		"suppress:\n  - events",
		"- \"spex-probe:dev\"",
		"docker build -f ",
		"/examples/integration/probe/Dockerfile -t spex-probe:dev ",
		"kindContext: \"local-dev\"",
		"skipClusterDelete: true",
		"kind load docker-image spex-probe:dev --name local-dev",
		"timeout: 300",
		"\ntimeout: 300\nstartKIND: true\n",
	} {
		if !strings.Contains(string(kuttlTest), want) {
			t.Fatalf("kuttl-test.yaml missing %q:\n%s", want, string(kuttlTest))
		}
	}
	setup, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "01-integration-setup.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(setup), "'helm' 'upgrade' '--install' 'redpanda' 'redpanda'") ||
		!strings.Contains(string(setup), "'--repo' 'https://charts.redpanda.com'") {
		t.Fatalf("setup step missing Helm command:\n%s", string(setup))
	}
	if !strings.Contains(string(setup), "kubectl --kubeconfig ../../kubeconfig create namespace spex-test") {
		t.Fatalf("setup step did not render portable kubeconfig path:\n%s", string(setup))
	}
	if !strings.Contains(string(setup), "test -d "+filepath.ToSlash(inputDir)) {
		t.Fatalf("setup step did not render integrationProfileDir:\n%s", string(setup))
	}
	if strings.Contains(string(setup), out) {
		t.Fatalf("setup step leaked absolute workspace path %q:\n%s", out, string(setup))
	}
	cleanup, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "00-rerun-cleanup.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cleanup), "'--kubeconfig' '../../kubeconfig'") {
		t.Fatalf("cleanup step did not render portable kubeconfig path:\n%s", string(cleanup))
	}
	if strings.Contains(string(cleanup), out) {
		t.Fatalf("cleanup step leaked absolute workspace path %q:\n%s", out, string(cleanup))
	}
	if _, err := os.Stat(filepath.Join(out, "kind.yaml")); err != nil {
		t.Fatalf("kind config was not copied: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(out, "README.generated.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Integration profile: enabled.",
		"`startKIND: true`",
		"`01-integration-setup.yaml`",
	} {
		if !strings.Contains(string(readme), want) {
			t.Fatalf("generated README missing %q:\n%s", want, string(readme))
		}
	}
	for _, rel := range []string{
		filepath.Join("kuttl", "mqtt-ingestion-basic", "02-static-configmaps.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "02-rbac.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "03-op-redpanda-snapshot-offsets.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "04-op-publish-reading-1.yaml"),
	} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Fatalf("missing shifted integration file %s: %v", rel, err)
		}
	}
}

func TestGenerateWorkspaceMaterializesLocalEnvFileSecretWithKINDKubeconfig(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, filepath.Join(dir, "inputs"), "localEnvFile", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `      keys:
        username: username
        password: password`, `      envFile: ../.secrets/kind.env
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
	inputs.Integration = &IntegrationProfile{Spec: IntegrationProfileSpec{
		KIND: KINDIntegration{Start: true, ClusterName: "local-dev"},
	}}
	out := filepath.Join(dir, "out")
	if err := Generate(out, inputs); err != nil {
		t.Fatal(err)
	}
	setup, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "01-integration-setup.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		". " + shellQuote(filepath.ToSlash(filepath.Join(dir, ".secrets", "kind.env"))),
		"kubectl '--kubeconfig' '../../kubeconfig' '-n' 'spex-test' 'create' 'secret' 'generic' 'mqtt-probe-credentials'",
		"| kubectl 'label' '--local' '-f' '-' '-o' 'yaml' 'spex/owned=true' 'spex/secret-id=mqtt-credentials'",
		"| kubectl '--kubeconfig' '../../kubeconfig' 'create' '-f' '-'",
	} {
		if !strings.Contains(string(setup), want) {
			t.Fatalf("secret materialization step missing %q:\n%s", want, string(setup))
		}
	}
}

func TestGenerateRejectsInvalidRunIDLabel(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, filepath.Join(dir, "inputs"), "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs.RunID = "bad/run"

	err = Generate(filepath.Join(dir, "out"), inputs)
	if err == nil || !strings.Contains(err.Error(), "runId must be a Kubernetes label value") {
		t.Fatalf("expected run ID label validation error, got %v", err)
	}
}

func TestGenerateQuotesKubernetesLabelValues(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, filepath.Join(dir, "inputs"), "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs.RunID = "true"
	out := filepath.Join(dir, "out")
	if err := Generate(out, inputs); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{
		filepath.Join("kuttl", "mqtt-ingestion-basic", "01-static-configmaps.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "03-op-publish-reading-1.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "01-rbac.yaml"),
	} {
		content, err := os.ReadFile(filepath.Join(out, rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			`spex/scenario: "mqtt-ingestion-basic"`,
			`spex/run-id: "true"`,
		} {
			if !strings.Contains(string(content), want) {
				t.Fatalf("%s missing quoted label %q:\n%s", rel, want, string(content))
			}
		}
	}
}

func TestGenerateQuotesStepMapStringValues(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, filepath.Join(dir, "inputs"), "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs.RunID = "true"
	out := filepath.Join(dir, "out")
	if err := Generate(out, inputs); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(filepath.Join(out, "step-map.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`scenario: "mqtt-ingestion-basic"`,
		`runId: "true"`,
		`kubeContext: "local-dev"`,
		`operationId: "publish-reading-1"`,
		`operationType: "mqtt.publish"`,
		`jobName: "spex-mqtt-ingestion-basic-03-publish-reading-1"`,
		`- "kuttl/mqtt-ingestion-basic/03-op-publish-reading-1.yaml"`,
	} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("step map missing quoted value %q:\n%s", want, string(content))
		}
	}
}

func TestGenerateQuotesManifestObjectReferences(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, filepath.Join(dir, "inputs"), "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs.RunID = "run-fixed-test"
	out := filepath.Join(dir, "out")
	if err := Generate(out, inputs); err != nil {
		t.Fatal(err)
	}

	job, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "03-op-publish-reading-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`name: "spex-mqtt-ingestion-basic-03-publish-reading-1"`,
		`namespace: "spex-test"`,
		`serviceAccountName: "spex-probe"`,
		`image: "spex-probe:dev"`,
		`imagePullPolicy: "IfNotPresent"`,
		`name: "spex-mqtt-ingestion-basic-payloads"`,
		`name: "mqtt-probe-credentials"`,
		`key: "username"`,
	} {
		if !strings.Contains(string(job), want) {
			t.Fatalf("Job missing quoted scalar %q:\n%s", want, string(job))
		}
	}

	rbac, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "01-rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`name: "spex-probe"`,
		`namespace: "spex-test"`,
		`name: "spex-mqtt-ingestion-basic"`,
		`verbs: ["get", "create", "update", "patch"]`,
	} {
		if !strings.Contains(string(rbac), want) {
			t.Fatalf("RBAC missing quoted scalar %q:\n%s", want, string(rbac))
		}
	}
	if strings.Contains(string(rbac), `"delete"`) {
		t.Fatalf("probe RBAC should not grant ConfigMap delete:\n%s", string(rbac))
	}
}

func TestGenerateQuotesCleanupShellArguments(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, filepath.Join(dir, "inputs"), "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs.RunID = "run-fixed-test"
	out := filepath.Join(dir, "out")
	if err := Generate(out, inputs); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "00-rerun-cleanup.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"'kubectl' '--context' 'local-dev' '-n' 'spex-test' 'delete' 'job'",
		"'-l' 'spex/owned=true,spex/scenario=mqtt-ingestion-basic'",
		"'delete' 'configmap'",
		"'-l' 'spex/owned=true,spex/scenario=mqtt-ingestion-basic,spex/runtime=true'",
		"'--ignore-not-found=true'",
	} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("cleanup step missing %q:\n%s", want, string(content))
		}
	}
}

func TestGenerateDefaultsServiceAccountConsistentlyWhenRBACCreateTrue(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, filepath.Join(dir, "inputs"), "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "    serviceAccountName: spex-probe\n", "", 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs.RunID = "run-fixed-test"
	out := filepath.Join(dir, "out")
	if err := Generate(out, inputs); err != nil {
		t.Fatal(err)
	}

	rbac, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "01-rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	job, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "03-op-publish-reading-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"01-rbac.yaml":                 string(rbac),
		"03-op-publish-reading-1.yaml": string(job),
	} {
		if !strings.Contains(content, "spex-probe") {
			t.Fatalf("%s missing default service account:\n%s", path, content)
		}
	}
	if !strings.Contains(string(job), `serviceAccountName: "spex-probe"`) {
		t.Fatalf("Job did not use generated default ServiceAccount:\n%s", string(job))
	}
}

func TestGenerateGraphQLJobSupportsKeycloakClientCredentials(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, filepath.Join(dir, "inputs"), "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
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
        - openid
        - profile`, 1))
	if err := os.WriteFile(bindingPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs.RunID = "run-fixed-test"
	out := filepath.Join(dir, "out")
	if err := Generate(out, inputs); err != nil {
		t.Fatal(err)
	}

	job, err := os.ReadFile(filepath.Join(out, "kuttl", "mqtt-ingestion-basic", "05-op-assert-reading-1-in-graphql.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`name: "SPEX_GRAPHQL_KEYCLOAK_CLIENT_SECRET"`,
		`name: "keycloak-client-credentials"`,
		`key: "client-secret"`,
		`"--keycloak-token-url=http://keycloak.identity.svc.cluster.local/realms/dev/protocol/openid-connect/token"`,
		`"--keycloak-client-id=spex"`,
		`"--keycloak-scope=openid"`,
		`"--keycloak-scope=profile"`,
	} {
		if !strings.Contains(string(job), want) {
			t.Fatalf("GraphQL Keycloak Job missing %q:\n%s", want, string(job))
		}
	}
	if strings.Contains(string(job), "SPEX_GRAPHQL_TOKEN") {
		t.Fatalf("Keycloak GraphQL Job should not mount direct bearer token:\n%s", string(job))
	}
}

func TestGenerateStaticConfigMapNamesAreDNSLabelsForLongScenarioName(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, filepath.Join(dir, "inputs"), "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	longName := "mqtt-ingestion-basic-with-a-very-long-scenario-name-that-needs-truncation"
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), "name: mqtt-ingestion-basic", "name: "+longName, 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs.RunID = "run-fixed-test"
	out := filepath.Join(dir, "out")
	if err := Generate(out, inputs); err != nil {
		t.Fatal(err)
	}

	scenarioSlug := DNSLabel(longName)
	staticConfigMaps, err := os.ReadFile(filepath.Join(out, "kuttl", scenarioSlug, "01-static-configmaps.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`name: "` + payloadsConfigMapName(scenarioSlug) + `"`,
		`name: "` + graphqlConfigMapName(scenarioSlug) + `"`,
		`name: "` + variablesConfigMapName(scenarioSlug) + `"`,
		`name: "` + matchersConfigMapName(scenarioSlug) + `"`,
	} {
		if !strings.Contains(string(staticConfigMaps), want) {
			t.Fatalf("static ConfigMaps missing %q:\n%s", want, string(staticConfigMaps))
		}
		name := strings.TrimSuffix(strings.TrimPrefix(want, `name: "`), `"`)
		if len(name) > 63 {
			t.Fatalf("generated ConfigMap name is too long: %q (%d)", name, len(name))
		}
	}

	job, err := os.ReadFile(filepath.Join(out, "kuttl", scenarioSlug, "03-op-publish-reading-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`name: "` + payloadsConfigMapName(scenarioSlug) + `"`,
		`name: "` + graphqlConfigMapName(scenarioSlug) + `"`,
		`name: "` + variablesConfigMapName(scenarioSlug) + `"`,
		`name: "` + matchersConfigMapName(scenarioSlug) + `"`,
	} {
		if !strings.Contains(string(job), want) {
			t.Fatalf("Job volume reference missing %q:\n%s", want, string(job))
		}
	}
}

func TestGenerateWorkspaceFollowsScenarioOperationOrder(t *testing.T) {
	dir := t.TempDir()
	scenarioPath, bindingPath := writeScenarioAndBinding(t, filepath.Join(dir, "inputs"), "kubernetesSecret", "tcp://emqx.platform.svc.cluster.local:1883")
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `    - id: assert-reading-1-in-redpanda
      type: redpanda.contains`, `    - id: publish-reading-2
      type: mqtt.publish
      mqtt:
        topic: telemetry/${param.deviceId}/readings
        payloadTemplateRef: valid-energy-reading
        correlationId: reading-2
    - id: assert-reading-1-in-redpanda
      type: redpanda.contains`, 1))
	if err := os.WriteFile(scenarioPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	inputs, err := LoadInputs(scenarioPath, bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs.RunID = "run-fixed-test"
	out := filepath.Join(dir, "out")
	if err := Generate(out, inputs); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		filepath.Join("kuttl", "mqtt-ingestion-basic", "03-op-publish-reading-1.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "04-op-publish-reading-2.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "05-op-assert-reading-1-in-redpanda.yaml"),
		filepath.Join("kuttl", "mqtt-ingestion-basic", "06-op-assert-reading-1-in-graphql.yaml"),
		filepath.Join("rendered", "payloads", "publish-reading-2.json"),
	} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
	stepMap, err := os.ReadFile(filepath.Join(out, "step-map.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stepMap), "ordinal: 4\n      operationId: \"publish-reading-2\"") {
		t.Fatalf("step map does not preserve inserted operation order:\n%s", string(stepMap))
	}
}
