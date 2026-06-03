# spex

spex lets testers describe acceptance scenarios once and generate an inspectable KUTTL workspace from that intent.

It is built for Kubernetes-based integration testing where the test author should not have to hand-write KUTTL steps, probe Jobs, ConfigMaps, or report mapping. The current implementation focuses on provider-shaped operations for MQTT, Redpanda, GraphQL, MongoDB, PostgreSQL, RabbitMQ, Redis, InfluxDB, optional Keycloak client-credentials auth, and Kind-backed local proof runs.

Status: production-candidate. Use it for controlled pilots and promotion pipelines that run the validation, security, artifact-scan, and live proof gates described in the production-readiness and live-proof docs.

![spex architecture](assets/spex_architecture.png)

This diagram shows how spex turns acceptance-test intent into a repeatable Kubernetes proof run.

Test authors describe the scenario once, using YAML, Gherkin features, GraphQL queries, suite configuration, and optional shared catalogs. Platform and test-infrastructure owners keep the environment-specific pieces separate: target bindings, integration profiles, secrets, and runtime settings.

spex connects both sides. It validates the suite, explains what will run, resolves the required references, and compiles an inspectable KUTTL workspace. From that workspace, spex generates the probe jobs and assertions that exercise the target system through provider capabilities such as MQTT, Redpanda, GraphQL, MongoDB, PostgreSQL, RabbitMQ, Redis, InfluxDB, and optional Keycloak authentication.

Teams can stop after compilation and review the generated workspace, or they can run the suite against kind or another Kubernetes target. During a live proof run, spex executes the generated probes, observes the system under test, collects results, and cleans up runtime resources.

The output is not just a pass/fail signal. spex produces scenario reports, KUTTL artifacts, logs, and optional resource-usage evidence so CI pipelines and reviewers can inspect what happened after the run.

## Commands

Check the installed binary:

```sh
spex version
spex version --format json
```

Create a scenario repository:

```sh
spex init scenario-repo --dir acceptance-tests
cd acceptance-tests
spex suite validate --suite suite.yaml
```

Validate and inspect a suite before running it:

```sh
spex suite list --suite suite.yaml
spex suite plan --suite suite.yaml
spex suite explain --suite suite.yaml
spex doctor --suite suite.yaml
```

`suite list` shows the scenario inventory. `suite plan` resolves bindings, profiles, catalogs, work/report paths, required secrets, required environment variables, and per-scenario operation counts. `suite explain` shows the operations that will be generated. `doctor` checks the suite and host prerequisites.

Compile or run a suite:

```sh
spex suite compile --suite suite.yaml
spex suite run --suite suite.yaml
spex suite run --suite suite.yaml --collect-resource-usage
```

`suite compile` writes an inspectable generated workspace without executing it. `suite run` runs the generated KUTTL workspace, collects Job status and logs, writes `reports/scenario-run-report.yaml`, and deletes generated Jobs and runtime offset ConfigMaps. Use `--retain-runtime-resources` when you want to inspect live Jobs and runtime ConfigMaps after a run. Use `--collect-resource-usage` to collect best-effort CPU/memory evidence with `kubectl top pod`; this writes `evidence/resources/*.pods.txt` and report `resourceRef` entries when metrics are available.

Inspect resolved integration bundles:

```sh
spex bundle list --suite suite.yaml
spex bundle explain --suite suite.yaml
```

`bundle list` shows locally resolved provider bundles. `bundle explain` shows registered capabilities, binding kinds, probe images, commands, and operation/result paths.

Suites can reference built-in bundles or local bundle directories:

```yaml
spec:
  bundleRefs:
    - name: redis
      version: 0.1.0
      source: builtin:redis

    - name: custom
      version: 0.1.0
      source: ../bundles/custom-echo
```

A local bundle directory contains `bundle.yaml`. Bundle capabilities register provider-qualified operation types, binding kinds, operation/result schemas, and declarative probe invocation metadata. Core still renders the KUTTL Job and writes the lowered operation file; bundles do not generate KUTTL directly.

```yaml
apiVersion: spex.bundle.v0.1
kind: IntegrationBundle
metadata:
  name: custom
  version: 0.1.0
spec:
  capabilities:
    - type: custom.echo
      bindingKind: custom.connection
      inputSchema:
        schema:
          type: object
          required: [message]
          properties:
            message:
              type: string
      resultSchema:
        schema:
          type: object
      probe:
        image: ghcr.io/pruefwerk/spex-probe-custom-echo:0.1.0
        command: [custom-probe, run]
        input:
          mode: operationFile
          path: /spex/input/operation.json
        output:
          path: /spex/output/result.json
  bindingSchemas:
    - kind: custom.connection
```

Git and OCI bundle sources are reserved for the locked external-bundle model and intentionally fail before bundle locking is implemented. See `examples/suites/redis-builtin-bundle.example.yaml` for a built-in bundle reference, and `examples/bundles/custom-echo/bundle.yaml` plus `examples/suites/custom-bundle-local.example.yaml` for a complete local bundle manifest and suite reference.

For local and external bundles, the bundle probe image is the runtime boundary and can be implemented in any language. spex only requires the lowered operation file input and normalized result envelope output described in `docs/probe-contract.md`. Built-in providers may still use the aggregate `spex-probe` image configured by the target binding for local demos and first-party compatibility.

First-party provider probes can also be built as standalone images. For example:

```sh
make redis-probe-image
```

This builds `spex-probe-redis:dev` from `examples/integration/probe-redis/Dockerfile`.

Inspect reusable catalogs:

```sh
spex catalog list --suite suite.yaml
spex catalog explain --suite suite.yaml
spex catalog check --suite suite.yaml
spex catalog docs --suite suite.yaml --out reports/catalog.md
```

`catalog list` shows available flow and step names with source files. `catalog explain` includes expansion counts for parameters, payload templates, GraphQL queries, and operations. `catalog check` validates catalog expressions. `catalog docs` generates tester-facing catalog documentation.

Most commands support JSON output for CI or dashboards:

```sh
spex suite plan --suite suite.yaml --format json
spex suite explain --suite suite.yaml --format json
spex doctor --suite suite.yaml --format json
spex catalog check --suite suite.yaml --format json
```

Export editor/CI schemas:

```sh
spex schema list
spex schema show scenario-suite > scenario-suite.schema.json
spex schema show scenario > scenario.schema.json
spex schema show target-binding > target-binding.schema.json
spex schema show integration-bundle > integration-bundle.schema.json
```

Available schema names are `scenario`, `scenario-suite`, `target-binding`, `integration-profile`, `integration-bundle`, `flow-catalog`, and `step-catalog`.

## Scenario Repositories

A scenario repository should contain tester-owned scenario intent and pipeline configuration, not generated KUTTL.

Minimal layout:

```text
suite.yaml
scenarios/
queries/
bindings/
generated/
reports/
```

Bootstrap one:

```sh
spex init scenario-repo --dir acceptance-tests
spex suite validate --suite acceptance-tests/suite.yaml
spex suite compile --suite acceptance-tests/suite.yaml
```

The scaffold includes `.schemas/*.schema.json`, `.vscode/settings.json`, `.gitignore`, `README.md`, `ci/spex-validate.sh`, `.github/workflows/spex.yaml`, and a Makefile with `doctor`, `validate`, `plan`, `explain`, `catalog`, `catalog-docs`, `compile`, `ci`, `run`, `clean`, and `schemas` targets. The generated workflow intentionally performs non-cluster checks only; platform teams can add a live `suite run` job once bindings, secrets, and cluster provisioning are available. Re-running `init scenario-repo` is intentionally non-destructive and fails if a scaffolded file already exists. The generated schemas intentionally catch common authoring mistakes early, including empty refs, empty matcher arrays, duplicate report formats, and empty secret key maps.

Keep generated workspaces, live reports, KUTTL artifacts, and kubeconfigs out of source control. The scaffolded `.gitignore` excludes `generated/` and `reports/`; CI may upload those as short-lived artifacts after `doctor --scan-artifacts` has run. The artifact scan fails if a file named `kubeconfig`, a file ending in `.kubeconfig`, or kubeconfig-shaped content is present.

The checked-in reference layout at `examples/reference-scenario-repo/` models the separate scenario repository shape with CI targets and an opt-in live proof workflow.

For a public, runnable example repository, see [pruefwerk/spex-testbench](https://github.com/pruefwerk/spex-testbench). It shows a scenario repository that uses the released `spex` binary in CI, validates tester-owned catalogs and suites, and runs the same scenario against a Kind-managed local test bench with MQTT, Redpanda, MongoDB, and GraphQL fixtures.

Add another scenario:

```sh
spex new scenario --dir acceptance-tests --name device-reading-is-queryable
```

The tester usually edits:

```text
scenarios/*.yaml
queries/*.graphql
suite.yaml
```

The platform or test-infra owner usually provides:

```text
bindings/*.yaml
integration profiles
kind/Helm/manifests for local proof stacks
Kubernetes Secrets in target namespaces
```

Suite files let CI execute a scenario set with one command:

```yaml
apiVersion: spex.suite.v0.1
kind: ScenarioSuite
metadata:
  name: mqtt-local
spec:
  bindingRef: bindings/dev.yaml
  scenarios:
    - scenarios/**/*.yaml
  workspaceDir: generated/mqtt-local
  failFast: false
  reports:
    outputDir: reports
    format:
      - yaml
      - json
      - junit
```

Run it:

```sh
spex suite run --suite suite.yaml
```

Suite runs write each generated workspace report under that workspace and aggregate suite-level reports when requested by `spec.reports.format`. `yaml` writes `suite-run-report.yaml`; `json` writes `suite-run-report.json`; `junit` writes `suite-junit.xml`; an empty or omitted format list writes all three. `spec.reports.format` is treated as a set, so duplicate formats fail validation. Scenario runs always write both `scenario-run-report.yaml` and `scenario-run-report.json`. Without `--out`, suite-level reports are written to `spec.reports.outputDir`; with `--out`, they are written under `<out>/reports`.

`bindingRef` and `integrationProfileRef` may point to files in the same scenario repo, absolute paths, `file://` paths, relative paths into another checked-out repository, or files stored in a Git repository:

```yaml
spec:
  bindingRef: ../../platform-test-targets/bindings/dev.yaml
```

Explicit Git URL form:

```yaml
spec:
  bindingRef: git::https://github.com/pruefwerk/platform-test-targets.git//bindings/dev.yaml@v1.2.3
  integrationProfileRef: git::https://github.com/pruefwerk/platform-test-targets.git//integration/local-kind.yaml@v1.2.3
```

For CI, use immutable tags or commit SHAs instead of mutable branches. `spex doctor --suite suite.yaml --require-pinned-git-refs` fails mutable external refs such as `@main`, `@master`, `@develop`, and `@dev`. Without `--require-pinned-git-refs`, those refs are reported as warnings. Git checkouts are cached under `.spex/git` by default, or under `SPEX_GIT_CACHE_DIR` when set.

The explicit form also works with `ssh://`, `https://`, and `file://` repository URLs.

GitHub Actions-style shorthand:

```yaml
spec:
  bindingRef: team/platform-test-targets/bindings/dev.yaml@v1.2.3
  integrationProfileRef: team/platform-test-targets/integration/local-kind.yaml@v1.2.3
```

For shorthand refs, the default base URL is `https://github.com`. Set this for GitHub Enterprise:

```sh
export SPEX_GIT_REF_BASE_URL=https://github.example.com
```

For GitHub Enterprise, replace the value with your own host, for example `https://github.example.com`.

Git refs are cloned into `.spex/git` next to the suite file by default. Override that cache location in CI if needed:

```sh
export SPEX_GIT_CACHE_DIR="$PWD/.cache/spex/git"
```

That keeps scenario repositories independent while still allowing centrally owned target bindings and integration profiles.

Reusable catalogs can be referenced the same way:

```yaml
spec:
  catalogRefs:
    - catalogs/telemetry-flow.yaml
    - git::https://github.com/pruefwerk/test-catalogs.git//telemetry-steps.yaml@v1.0.0
```

Flow catalogs let testers use compact scenario YAML that expands to the strict ScenarioModel:

```yaml
spec:
  use:
    - flow: mqttToRedpandaToGraphql
      id: reading-1
      with:
        tenantId: tenant-dev
        deviceId: device-dev-1
        value: "42.5"
```

Step catalogs are the pre-Gherkin bridge. They map controlled text expressions to generated ScenarioModel operations without recompiling the binary:

```yaml
spec:
  stepInvocations:
    - kind: when
      text: device "device-dev-1" publishes energy reading 42.5 as "reading-1"
    - kind: then
      text: Redpanda contains reading "reading-1" with value 42.5
```

Inspect what a suite expands to:

```sh
spex suite list --suite suite.yaml
spex suite list --suite suite.yaml --format json
spex suite plan --suite suite.yaml
spex suite plan --suite suite.yaml --format json
spex suite explain --suite suite.yaml
spex suite explain --suite suite.yaml --format json
spex doctor --suite suite.yaml
spex doctor --suite suite.yaml --format json
spex suite run --suite suite.yaml --collect-resource-usage
spex catalog list --suite suite.yaml
spex catalog list --suite suite.yaml --format json
spex catalog explain --suite suite.yaml
spex catalog explain --suite suite.yaml --format json
spex catalog check --suite suite.yaml
spex catalog check --suite suite.yaml --format json
spex catalog docs --suite suite.yaml --out reports/catalog.md
```

`suite list` gives a compact scenario inventory by default; `--format json` returns a stable machine-readable inventory for CI and dashboards. `suite plan` adds binding/profile/catalog resolution, work/report paths, Helm apps, required secrets, required environment variables, and per-scenario operation counts. `suite explain` shows the resolved operations per scenario; `suite explain --format json` provides the same expansion as a stable artifact for review tooling. `doctor` performs host and suite preflight checks; `doctor --format json` emits the same checks for CI systems that want to archive or parse preflight results. `catalog list` shows flow and step names with their source files. `catalog explain` includes flow parameters and expansion counts for parameters, payload templates, GraphQL queries, and operations, so parameter-only `Given` setup steps are visible. The catalog inspection commands support `--format json`; `catalog check --format json` reports `status`, flow/step counts, and any failures for CI. `catalog docs` generates tester-facing catalog documentation with the same expansion counts.

The binary also embeds JSON Schemas for editor and CI integration:

```sh
spex schema list
spex schema list --format json
spex schema show scenario-suite > scenario-suite.schema.json
spex schema show scenario > scenario.schema.json
spex schema show target-binding > target-binding.schema.json
spex schema show integration-bundle > integration-bundle.schema.json
```

Available schema names are `scenario`, `scenario-suite`, `target-binding`, `integration-profile`, `integration-bundle`, `flow-catalog`, and `step-catalog`. Use `schema list --format json` when a CI job or editor bootstrap script needs a stable machine-readable schema inventory.

Suites may also include constrained Gherkin `.feature` files:

```yaml
spec:
  catalogRefs:
    - catalogs/telemetry-steps.yaml
  scenarios:
    - features/**/*.feature
```

Feature files are parsed into `stepInvocations` and then expanded through `StepCatalog`. The binary does not execute arbitrary step functions and does not generate KUTTL directly from free-form text. Tags can be used for suite filtering with `--include-tag` and `--exclude-tag`:

```gherkin
@smoke @mqtt
Feature: MQTT ingestion

  Background:
    Given tenant "tenant-dev"
    And device "device-dev-1"

  @graphql
  Scenario Outline: MQTT reading reaches Redpanda and GraphQL
    When device "device-dev-1" publishes energy reading <value> as "<correlationId>"
    Then Redpanda contains reading "<correlationId>" with value <value>
    And GraphQL returns reading "<correlationId>" for device "device-dev-1" with value <value>

    Examples:
      | correlationId | value |
      | reading-1     | 42.5  |
      | reading-2     | 11.0  |
```

Gherkin support is intentionally constrained to tags, `Background`, multiple `Scenario` blocks per `.feature` file, `Scenario Outline` with one `Examples` table, and `Given`/`When`/`Then`/`And`/`But` steps backed by catalog expressions.
If a step does not match any catalog expression, validation fails with the unmatched text and the available expressions for that step kind.

## Provider Operations

spex supports provider-shaped operations through the generic operation IR:

```yaml
operations:
  - id: assert-reading-in-influxdb
    type: influxdb.expect
    timeout: 60s
    with:
      bindingRef: influxdb.main
      language: flux
      query: |
        from(bucket: "telemetry")
          |> range(start: -1h)
          |> filter(fn: (r) => r.correlationId == "reading-1")
          |> limit(n: 1)
      match:
        - path: $.rows[0].correlationId
          equalsString: reading-1
```

Provider operations always use a fully qualified `type` such as `redis.assertValueEquals` or `influxdb.expect`. The `with` object is validated by the provider capability schema, then lowered into one operation file for the probe container. The probe writes one normalized result envelope that spex validates and includes in reports.

Generic bindings keep target configuration separate from scenario intent:

```yaml
spec:
  secrets:
    influxdb-credentials:
      type: kubernetesSecret
      name: influxdb-probe-credentials
      keys:
        token: token
  bindings:
    - name: influxdb.main
      kind: influxdb.connection
      with:
        version: v2
        endpoint: http://influxdb.platform.svc.cluster.local:8086
        org: dev
        credentialsRef: influxdb-credentials
```

InfluxDB uses `SPEX_INFLUXDB_TOKEN` inside the generated probe Job. For `version: v2`, use Flux queries and provide `org`. For `version: v3`, use `language: sql` or `language: influxql` and provide `database` instead of `org`:

```yaml
with:
  bindingRef: influxdb.main
  language: sql
  query: select * from readings limit 1
  match:
    - path: $.rows[0].correlationId
      equalsString: reading-1
```

See `examples/providers/influxdb/influxdb-reading-exists.yaml`, `examples/bindings/influxdb-local.yaml`, and `examples/suites/influxdb-local.example.yaml`.

## Integration Proof

The live proof compiles a KUTTL workspace that can own Kind and stack setup natively. The integration profile is rendered into `kuttl-test.yaml` and setup `TestStep` files: `startKIND`, `kindConfig`, `kindContainers`, top-level KUTTL commands, and Helm/app setup commands live in generated KUTTL.

The cluster needs MQTT, Redpanda, GraphQL, and optionally Keycloak endpoints matching the selected binding. Those components should be deployed as real services through KUTTL setup commands, usually Helm installs or manifest applies. Fakes are not part of the default path; use them only when a scenario explicitly requests a fake or WireMock-style dependency.

`examples/integration/local-kind-profile.yaml` and `examples/integration/local-kind-keycloak-profile.yaml` are runnable local proof profiles. They build the probe and demo stack images, load them into a KUTTL-managed Kind cluster, apply real local manifests for EMQX, Redpanda, GraphQL, and optionally Keycloak, then run the generated KUTTL scenario.

The example Kind profiles create the scenario namespace as the first setup command. Without an integration profile, the runner assumes the namespace already exists.

The example profiles also create the probe Kubernetes Secrets from host environment variables during KUTTL setup. Generated workspaces contain only variable names such as `$SPEX_MQTT_PASSWORD`, not the secret values.
When `INTEGRATION_RUN_KUTTL=true`, `scripts/integration_live.sh` preflights any `SPEX_*` host variables referenced by the selected integration profile and fails before compile/run if one is missing.
For live runs, the script also preflights host tools referenced by the profile commands: `docker`, `helm`, `kind`, and `kubectl`.

The CLI uses stable exit-code classes for automation:

```text
0 passed
1 internal/tool error
2 validation/configuration failure
3 runtime scenario failure
4 environment/preflight failure
```

```sh
export SPEX_MQTT_USERNAME=...
export SPEX_MQTT_PASSWORD=...
export SPEX_GRAPHQL_TOKEN=...
make integration-example
```

Run the Keycloak-backed live proof:

```sh
export SPEX_MQTT_USERNAME=...
export SPEX_MQTT_PASSWORD=...
export SPEX_KEYCLOAK_ADMIN_PASSWORD=...
export SPEX_GRAPHQL_KEYCLOAK_CLIENT_SECRET=...
make integration-example-kind-keycloak
```

Compile with a KUTTL-native integration profile:

```sh
INTEGRATION_PROFILE=examples/integration/local-kind-profile.yaml \
make integration-example
```

You can also keep the integration settings in a profile file:

```sh
cp examples/integration/local-kind.env.example examples/integration/local-kind.env
INTEGRATION_CONFIG=examples/integration/local-kind.env make integration-example
```

Values in `INTEGRATION_CONFIG` are defaults. Explicit environment variables on the command line take precedence.

To validate and render a real profile without starting kind or running KUTTL yet, use compile-only mode:

```sh
INTEGRATION_RUN_KUTTL=false \
INTEGRATION_CONFIG=examples/integration/local-kind.env \
make integration-example
```

Integration profile command strings support these placeholders:

```text
${repoRoot}
${integrationProfileDir}
${workspaceDir}
${namespace}
${kubeContext}
${probeImage}
${probeImagePullPolicy}
${kindCluster}
```

Use `${repoRoot}` for files owned by the scenario repository and `${integrationProfileDir}` for files shipped next to the integration profile, such as reusable Helm charts in a shared integration catalog repository.

For KUTTL-managed `startKIND` runs, keep `${kindCluster}` aligned with the cluster KUTTL creates. The example uses `kind`, so the generated kube context is `kind-kind`.

Integration profiles can be composed with `spec.extends`:

```yaml
spec:
  extends:
    - base-kind.yaml
  helmApps:
    - name: my-service
      chart: my-service
      repo: https://charts.example.com/team
```

Parent `kind` commands, setup commands, containers, and Helm apps are appended before the child profile, while scalar child fields override parent scalar fields.

Integration profile shape:

```yaml
apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  allowFakes: false
  kind:
    start: true
    clusterName: kind
    config: local-kind.yaml
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
      - command: helm upgrade --install redpanda ./charts/redpanda --namespace ${namespace} --kube-context ${kubeContext} --create-namespace --wait
        timeout: 300
```

`spec.kind.config` is resolved relative to the profile file and copied into the generated workspace as `kind.yaml`. `spec.kind.clusterName` must be a DNS-1123 label; when `spec.kind.start` is true and `clusterName` is set, the kube context must be `kind-<clusterName>`.
Use `${kubeContext}` explicitly in host-side Helm and kubectl integration commands so setup never depends on the operator's current context.

Integration setup commands must deploy real services by default. Commands that reference fake/mock service names such as WireMock are rejected unless the profile explicitly sets `spec.allowFakes: true`.

Integration commands must not embed credentials directly. Literal secret values in `kubectl create secret --from-literal=...` and URL userinfo such as `https://user:password@example.com/...` fail validation. Use host environment variable references instead.

Integration profiles may install Helm charts without writing raw shell commands:

```yaml
apiVersion: spex.integration.v0.1
kind: IntegrationProfile
spec:
  helmApps:
    - name: my-service
      chart: my-service
      repo: https://charts.example.com/team
      namespace: application
      values:
        - ${repoRoot}/integration/values/my-service.yaml
      set:
        image.tag: "1.2.3"
      wait: true
      timeout: 300s
```

For OCI archives or local chart directories, omit `repo` and put the full chart reference in `chart`.

The generator renders these as validated `helm upgrade --install` setup commands in the generated KUTTL workspace.

The example setup commands create these probe Secrets from host environment variables:

```text
mqtt-probe-credentials:
  username <- SPEX_MQTT_USERNAME
  password <- SPEX_MQTT_PASSWORD

graphql-probe-credentials:
  token <- SPEX_GRAPHQL_TOKEN

keycloak-client-credentials:
  client-secret <- SPEX_GRAPHQL_KEYCLOAK_CLIENT_SECRET
```

For Keycloak-backed GraphQL:

```sh
export SPEX_MQTT_USERNAME=...
export SPEX_MQTT_PASSWORD=...
export SPEX_GRAPHQL_KEYCLOAK_CLIENT_SECRET=...
make integration-example-keycloak
```

For a KUTTL-managed kind run that also deploys Keycloak through the integration profile:

```sh
export SPEX_MQTT_USERNAME=...
export SPEX_MQTT_PASSWORD=...
export SPEX_GRAPHQL_KEYCLOAK_CLIENT_SECRET=...
INTEGRATION_PROFILE=/path/to/real-local-kind-keycloak-profile.yaml \
make integration-example-kind-keycloak
```

Useful overrides:

```sh
PROBE_IMAGE=spex-probe:dev
PROBE_IMAGE_PULL_POLICY=IfNotPresent
KUBE_CONTEXT=kind-kind
START_KIND_IN_KUTTL=false
INTEGRATION_ALLOW_PLACEHOLDER_PROFILE=false
INTEGRATION_RUN_KUTTL=true
INTEGRATION_PROFILE=examples/integration/local-kind-profile.yaml
KUBECTL=kubectl
SPEX=
NAMESPACE=spex-test
INTEGRATION_CONFIG=
REPO_ROOT=
```

Set `SPEX=/path/to/spex` to use a prebuilt CLI instead of `go run ./cmd/spex` inside the integration script.
Use `REPO_ROOT=...` with the integration script, or `spex compile --repo-root ...` directly, when you need deterministic generated examples. Local compiles default `${repoRoot}` to the current working directory.

## Scope

Probe Jobs consume Kubernetes Secrets. Bindings may either reference pre-existing Secrets with `type: kubernetesSecret`, or ask the generated KUTTL setup step to materialize the Secret before probe Jobs run.

Local env-file materialization:

```yaml
spec:
  secrets:
    mqtt-credentials:
      type: localEnvFile
      name: mqtt-probe-credentials
      envFile: .secrets/local.env
      env:
        username: SPEX_MQTT_USERNAME
        password: SPEX_MQTT_PASSWORD
      keys:
        username: username
        password: password
```

AWS SSM materialization:

```yaml
spec:
  secrets:
    mqtt-credentials:
      type: awsSsmParameter
      name: mqtt-probe-credentials
      ssmParameters:
        username: '{{ ssm "team/dev/mqtt/username" }}'
        password: /team/dev/mqtt/password
      keys:
        username: username
        password: password
```

The generated setup step creates a labeled Kubernetes Secret and fails if a Secret with the same name already exists; generated manifests and reports contain only Secret names, key names, env var names, and SSM parameter paths, not secret values. `ssmParameters` accepts either a raw SSM parameter name or the Helm-style `{{ ssm "path/to/key" }}` form.

MQTT broker URLs can also come from SSM when the full URL is environment-specific:

```yaml
spec:
  mqtt:
    brokerURL: '{{ ssm "/team/dev/mqtt/broker_url" }}'
    credentialsRef: mqtt-credentials
```

For SSM-backed MQTT broker URLs, the generated setup step materializes the value as a labeled Kubernetes Secret. Probe jobs read the URL from that Secret at runtime instead of rendering it into the generated Job arguments.

GraphQL auth supports either a pre-existing bearer token Secret or Keycloak client-credentials. For Keycloak, the binding supplies the token endpoint and client ID, while the client secret comes from a Kubernetes Secret.

MongoDB assertions use `mongodb.expect` operations. The scenario supplies a collection, a JSON filter, a correlation ID, and matchers. The binding supplies `spec.mongodb.uri`, `spec.mongodb.database`, and optionally `spec.mongodb.credentialsRef` with `username` and `password` keys. Filters and matchers support the same `${scenarioRunId}`, `${correlationId}`, and `${param.<name>}` template values used by the other assertion operations.

MongoDB Atlas is supported as a pre-existing external target. Set `spec.mongodb.deployment: atlas`, use a credential-free `mongodb+srv://` URI, and provide `spec.mongodb.credentialsRef`. spex does not create or destroy Atlas clusters; network access, allow lists, users, and database lifecycle stay outside generated KUTTL setup.

PostgreSQL assertions use `postgresql.expect` operations. The scenario supplies a SQL query, optional string arguments, a correlation ID, and matchers. The binding supplies `spec.postgresql.uri` and optionally `spec.postgresql.credentialsRef` with `username` and `password` keys. Queries, arguments, and matchers support `${scenarioRunId}`, `${correlationId}`, and `${param.<name>}` template values.

RabbitMQ test benches connect through bindings and Secrets. RabbitMQ expectations should target dedicated test queues because matching consumes inspected messages.

## License

spex is source-available under the Internal Business Source Available License 1.0.

The public license allows internal business use, including internal development, testing, CI/CD validation, QA, staging, proof-of-concept work, and internal production use, subject to the full terms in `LICENSE`.

It does not allow commercial redistribution, resale, external hosted or managed offerings, embedding spex in a third-party product or service, making spex available to third parties, or AI training use without a separate commercial license.

Generated scenarios, generated KUTTL workspaces, generated reports, and other output produced by spex may be used for permitted internal business operations, as long as the output does not redistribute, expose, or provide spex itself as an external offering.

spex is provided as is. You are responsible for deciding whether it is suitable for your environment, validating generated workspaces before running them, and accepting the risk of any damage, data loss, outage, misconfiguration, or other consequence from using it. The full warranty disclaimer and liability limits are in `LICENSE`.

See [LICENSE](LICENSE), [COMMERCIAL.md](COMMERCIAL.md), [CONTRIBUTING.md](CONTRIBUTING.md), and [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md).
