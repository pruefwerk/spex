# Failure Modes

Use this page to classify failed production runs before changing scenarios or bindings.

## Validation Failure

The scenario, binding, suite, catalog, or integration profile failed static validation.

Common causes:

- unknown field
- unsupported API version or kind
- mutable Git refs when `--require-pinned-git-refs` is enabled
- missing required secret reference
- embedded credentials in URLs
- invalid Kubernetes names or labels
- GraphQL query does not use `$scenarioRunId` and `$correlationId`

Fix the authoring input. Do not debug the cluster yet.

## Workspace Completeness Failure

The runner could not load the generated workspace, usually because `step-map.yaml` is missing, malformed, too large, or not a regular file.

Regenerate the workspace from the scenario and binding. Do not edit generated files by hand.

## KUTTL Execution Failure

KUTTL or kubectl could not start, or KUTTL failed before a mapped Job existed.

Check:

- `kubectl` path and permissions
- kubeconfig and context
- namespace permissions
- KUTTL installation
- generated manifest path mentioned in the error

If KUTTL output identifies a generated file, the report maps the failure to that generated step.

## Probe Job Failure

A generated Job ran and failed. The report maps it through Job status, Pod termination state, probe result JSON, and logs.

Check:

- `evidence/status/*.job.json`
- `evidence/logs/*.log`
- `evidence/results/*.jsonl`
- generated Job manifest for the operation

## MQTT Publish Failure

Likely causes:

- broker URL wrong for the target namespace
- MQTT Secret missing or wrong key names
- broker rejects credentials
- network policy blocks probe Pod
- topic name mismatch

## Redpanda Assertion Failure

Likely causes:

- Redpanda bootstrap address is wrong from inside the cluster
- expected topic is wrong
- ingestor did not write the event
- correlation ID is missing or changed
- the event was produced before the offset snapshot

The assertion scans from captured offsets, not from earliest. Re-run only after understanding whether the publish step happened after the snapshot.

## GraphQL Assertion Failure

Likely causes:

- endpoint is wrong from inside the cluster
- query does not return the expected fields
- variables do not match generated payload values
- resolver has not observed the event yet
- Keycloak token acquisition failed
- token Secret is missing or wrong

For Keycloak, inspect token acquisition output in the GraphQL probe logs.

## Missing Pod Logs

The report may show `pod_log_collection_missing_pod` when KUTTL reported a failure but no mapped Pod logs could be collected.

Check:

- Pod was deleted before evidence collection
- labels in `step-map.yaml` match generated Job labels
- namespace is correct
- kubectl permissions allow `logs`

## Runtime Cleanup Failure

The scenario may have passed, but cleanup failed. The report result becomes `error` with `runtime_cleanup_failed`.

Check:

- delete permissions for generated Jobs and runtime ConfigMaps
- namespace still exists
- kubeconfig/context still valid

Use `spex clean --workspace <workspace> --all` after fixing permissions.

## Resource Usage Collection Failure

Resource usage evidence is best-effort. Missing `evidence/resources/*.pods.txt` usually means metrics-server is unavailable or `kubectl top` lacks permissions.

This does not invalidate functional assertions unless your pipeline explicitly requires resource evidence.
