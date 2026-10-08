# Scenario runtime v1 qualification

Date: 8 October 2026. Release candidate: `v0.1.0-rc.41`.

## Implementation

Spex owns scenario parsing, canonicalization, identity, runtime selection and
execution. The scenario schema remains `spex.scenario/v1`. The in-process runtime
contract separates resolution, planning and execution; the registry does not load
plugins or interpret migration-testbench fields.

The configured host, `spex runtime --config spex-runtime.toml`, supplies the shared
execution implementation. Migration-testbench provides trusted configuration,
source and scheduling policies, service fixtures, deployment scripts and tests.
It no longer compiles a separate host or maintains production Python adapters.
Suite, scenario, commit-tag, local and receiver entrypoints share that host.

The existing resolver still owns inherited suite, binding and profile values.
The typed scenario overlay applies last. Unspecified fields inherit; explicit
scalar values replace inherited values. The Action transports definitions to the
receiver; it does not select a Spex release or implement runtime semantics.
The receiver owns the executable version and authorizes caller identities.

Changed Spex packages include `internal/command`, `pkg/runtimehost`,
`pkg/runtimehelpers`, `pkg/ownedkind`, `pkg/scheduling`, `pkg/receiver`,
`pkg/scenarioruntime` and suite inspection. Host instances own defensive snapshots
of their configuration. The host serializes inherited environment-dependent
operations and restores its environment after success, failure and cancellation.

## Execution and evidence

Canonical TOML identifies the scenario independently of runner paths and execution
IDs. Execution artifacts retain the canonical scenario, plan, structured result
and test evidence. Secret expansion stays outside canonical authoring; diagnostic
and artifact tests check redaction. Cleanup records secondary problems without
replacing the primary test outcome.

Kind execution uses unique scopes and exact ownership ledgers for clusters and
images. A live shared-daemon check verified cancellation cleanup without deleting
the surviving execution's resources. Optional MongoDB capacity scheduling admits
execution only when configured; an absent URI disables scheduling, while an
invalid or unreachable configured URI blocks admission.

AWS uses an existing shared stack, not an isolated deployment. The report separates
owned temporary resources, persistent test data and operator responsibilities.
The current host refuses AWS execution until its shared-stack integration exists;
static validation does not establish AWS acceptance.

## Qualification status

`PASS` below describes the stated local scope, not a successful company-hosted
deployment. Earlier successful gateway acceptance runs predate the host extraction
and do not qualify this candidate's complete private-chart suite.

| Capability | Result | Evidence or remaining gate |
| --- | --- | --- |
| Existing migration-testbench invocation | PASS | Host compatibility and project black-box checks; hosted rerun pending. |
| Scenario TOML | PASS | Parser, canonicalization and strict validation tests. |
| Action-generated scenario | PASS | Local adapter and mocked transport qualification. |
| Inline definitions | PASS | Source parsing, projection and execution-path tests. |
| External definitions | PASS | File projection, path validation and transport tests. |
| Committed scenario | PASS | Builder, override and runtime selection tests. |
| Inherited configuration | PASS | Existing resolver and empty-overlay parity tests. |
| Scenario precedence | PASS | Typed final-overlay tests. |
| Typed runtime configuration | PASS | Strict host and overlay decoding; unknown fields fail. |
| Local reproduction | PASS | Shared command path and built-executable checks. |
| Canonical artifact | PASS | Identity, artifact and result-schema tests. |
| Secret redaction | PASS | Sentinel and failure-path regression tests. |
| Cancellation | PASS | Unit/race checks and live overlapping Kind cleanup. |
| Failure classification | PASS | Primary/secondary outcome tests. |
| Full acceptance suite | BLOCKED | Fresh private-chart run requires company GitHub/ECR. |
| Hosted Action → receiver execution | BLOCKED | Explicit caller policy and authorized source-read credentials required. |
| AWS shared-stack execution | BLOCKED | Host integration remains unimplemented; execution fails closed. |

The candidate passes root and HTTP-probe race tests, `go vet`, module verification
and vulnerability checks. The root scan reports no reachable vulnerability;
non-reachable dependency advisories remain visible in scanner output.
Release qualification checks archive integrity and the packaged executable.
Migration-testbench validation covers 24 suite entrypoints and its 42-step catalog,
including 73 gateway-migration scenarios. Workflow lint checks pass.

GitHub rejected rc.40 before publication. Its second test invocation inherited
temporary scope variables because a unit fixture wrote to the enclosing job's
`GITHUB_ENV`. The fixture now isolates GitHub command files, with a regression
check. GitHub also exposed cancellation between the scheduler's context check
and its admission transaction. The scheduler now reconciles a confirmed queued
entry using a fresh context; uncertain running slots remain reserved. Deterministic
tests cover both outcomes. rc.40's tag remains intact but has no published release.

## Publication gates and limitations

Publish the candidate through the release workflow, verify its recorded commit
and checksums, then update all testbench executable, HTTP-bundle and Go-module pins
together. Qualify testbench without a local Go workspace override. Run a selected
commit-tag group in company CI before treating the extracted host as deployment
qualified. Positive receiver qualification must use an explicitly approved caller;
the default policy intentionally rejects every caller.

This report does not claim complete v1 acceptance qualification. The machine-readable
[blocker record](blockers.json) tracks unresolved hosted gates. No dynamic plugins,
scenario inheritance, generic workflow engine, distributed executor or runtime
marketplace forms part of this release.

## Usage documentation

Use the three primary authoring examples in the scenario documentation: inline
definitions, external files and a committed scenario. See
[configured host](../../configured-runtime-host.md),
[host execution](../../host-managed-execution.md),
[runtime SDK](../../runtime-host-sdk.md),
[ownership](../../owned-kind.md) and [scheduling](../../scheduling.md).
Receiver examples illustrate the contract; the runtime repository owns its real
workflow and trust policy.
