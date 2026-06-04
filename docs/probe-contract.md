# Probe Contract

spex core is responsible for orchestration. Probe containers are responsible for provider-specific runtime behavior.

The contract is intentionally language-agnostic. A probe image can be implemented in Go, Python, Node, Rust, Java, or another runtime as long as it follows the same file boundary.

## Inputs

spex writes one lowered operation JSON file per generated operation and mounts it into the probe container.

Minimum shape:

```json
{
  "operationId": "assert-cache-value",
  "operationType": "redis.assertValueEquals",
  "provider": "redis",
  "binding": {
    "name": "redis.main",
    "kind": "redis.connection",
    "with": {}
  },
  "with": {},
  "timeout": "30s",
  "dependsOn": []
}
```

The bundle capability declares where that file is mounted:

```yaml
probe:
  input:
    mode: operationFile
    path: /spex/input/operation.json
```

When the input path includes a directory, spex mounts the operation ConfigMap at that directory and passes the generated operation file path through `--operation-file`.

## Outputs

The probe writes one normalized result envelope JSON file.

Minimum shape:

```json
{
  "operationId": "assert-cache-value",
  "operationType": "redis.assertValueEquals",
  "provider": "redis",
  "status": "passed",
  "result": {},
  "evidence": [],
  "diagnostics": []
}
```

The bundle capability declares where the result file is written:

```yaml
probe:
  output:
    path: /spex/output/result.json
  env:
    CUSTOM_URI:
      fromBinding: uri
    CUSTOM_TOKEN:
      secretRef: credentials.token
    CUSTOM_MODE:
      value: strict
```

spex owns the envelope fields. Provider schemas validate only `operation.with` and `result`.

## Invocation

Bundle probe recipes are declarative:

```yaml
probe:
  image: ghcr.io/pruefwerk/spex-probe-redis@sha256:...
  command: ["spex-probe-redis", "run"]
  args: []
  input:
    mode: operationFile
    path: /spex/input/operation.json
  output:
    path: /spex/output/result.json
```

spex renders a standard Kubernetes Job and appends these flags:

```text
--operation-file=<lowered-operation-file>
--result-file=<result-file>
--timeout=<operation-timeout>
--poll-interval=<suite-poll-interval>
```

For local or external bundles, the bundle probe image is the runtime boundary and takes precedence over the aggregate target binding probe image. Built-in providers may still use the aggregate `spex-probe` image configured by the target binding.

Declarative env entries are resolved by spex core. `value` emits a literal value, `fromBinding` reads a string field from the resolved operation binding, and `secretRef: credentials.<key>` reads `<key>` from the binding's `credentialsRef` secret.

First-party provider probes may be shipped both ways:

```text
spex-probe redis run ...       # aggregate image entrypoint
spex-probe-redis run ...       # provider-specific image entrypoint
spex-probe influxdb run ...    # aggregate image entrypoint
spex-probe-influxdb run ...    # provider-specific image entrypoint
```

Both forms consume the same lowered operation file and write the same normalized result envelope. The provider-specific form is the target shape for standalone probe images.

## Exit Behavior

The probe should write a normalized result envelope before exiting whenever possible.

Recommended behavior:

```text
status=passed  -> operation satisfied
status=failed  -> operation ran but assertion failed
nonzero exit   -> probe/runtime failure before a usable envelope
```

For assertion failures, include diagnostics:

```json
{
  "severity": "error",
  "message": "redis key \"cache:user-123\" value \"pending\" does not equal \"active\""
}
```

## Boundary Rule

Bundles do not generate KUTTL directly and do not execute inside the spex process. They describe provider capabilities and probe invocation metadata. spex core lowers operations, renders Jobs, collects results, validates envelopes, and aggregates reports.
