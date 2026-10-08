# Scenario runtime v1 qualification

Updated: 9 October 2026. Published release candidate: `v0.1.0-rc.43`.

## rc.43 release qualification

The organization source selector replaces mandatory per-workflow, branch and actor
lists for internal hosts. GitHub-verified ownership and non-public visibility form
the coarse boundary; finer restrictions remain optional. Existing exact-repository
policies, integrity checks, source correlation and replay protection remain supported.

Tag `v0.1.0-rc.43` points to `da17ed307457a954326d4124e55b4fea0b7eaa4f`.
[Release run 37854675727](https://github.com/pruefwerk/spex/actions/runs/37854675727)
passed security, race/static analysis, production-candidate and packaging gates.
Downloaded binary and HTTP-bundle checksums passed; version and provenance records
match the tag's commit. Local qualification passed 806 root race tests and 37 HTTP
bundle tests.

Migration-testbench commit `f7499e5` updates tooling, HTTP-bundle and qualification
module pins together. Standalone race/static/module qualification passed against
the published rc.43 module, including decoding the deployed organization policy.
All 24 suite entrypoints and the 42-step catalog validated; ownership selection
contains ten scenarios. The commit selects ownership-only CI. Hosted acceptance
and positive caller-to-receiver execution remain separate, outstanding gates.

The remaining sections retain the earlier campaign's evidence and limitations.

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

rc.41 is [published](https://github.com/pruefwerk/spex/releases/tag/v0.1.0-rc.41).
Its [release workflow](https://github.com/pruefwerk/spex/actions/runs/37837425065)
passed. Downloaded archive checksums, version metadata and provenance identify
commit `b5e714280e5a7f964610cf474cc280d6a204bdbf`.

Testbench commit `9162d7e` updated executable, HTTP-bundle and Go-module pins
together. Standalone qualification used `GOWORK=off` and the downloaded rc.41
module; no local replacement supplied the SDK. All suites, the catalog, selected
ownership scenarios, 70 Action tests and live overlapping Kind isolation passed
locally against that version.

The first enterprise qualification exposed its runner's `CGO_ENABLED=0` default.
Testbench commit `f256c0e` explicitly enables CGO for race tests and prepares the C
compiler in CI. It also fixes a reachable fixture `x/text` finding, updates the
fixture builder, and adds fixture race/security gates. All 160 fixture race tests,
static analysis, Linux compilation and a fresh scan passed locally; the scan
reports no reachable vulnerabilities, not an absence of dependency advisories.
Testbench commit `d75acbb` also removes the unit fixtures' accidental dependency
on a downloaded `.spex/http-bundle`. Fixtures now use assets from the pinned SDK
module. All 44 project race checks passed against a clean Git snapshot without
downloaded assets. Hosted runtime qualification run `37839862617` passed,
including fixture race/security and bootstrap syntax checks. Ownership run
`37839862440` passed selection and validation but failed before deployment. A
repeat run retained canonical evidence after testbench `163535c` removed an upload
condition tied to the authoring mode.

Testbench `b3257ed` fixes the adapter's working directory: execution operations now
enter the trusted checkout, while other helpers retain caller-relative paths.
The nested-workspace regression failed before the fix and passed afterward; all
project race/static checks passed. Hosted acceptance has restarted.

rc.42 additionally retains allowlisted Spex failure codes in safe evidence. It
still excludes backend messages and unknown codes. Sentinel tests, root/HTTP-probe
security and race gates, static analysis and archive qualification passed locally.
rc.42 is [published](https://github.com/pruefwerk/spex/releases/tag/v0.1.0-rc.42).
Release run `37845026810` passed. Downloaded archive checksums and provenance
identify commit `0b0861525435ed069e076081c557db0c902088bc`. Testbench `580ba30`
updates tool, HTTP-bundle and qualification-module pins together. Standalone
project race/static checks, all suite/catalog validators and 70 Action tests passed
against rc.42. Ownership-only acceptance run `37845674927` is queued; no full-suite
or receiver success follows from these checks.

Finish that selected-group run before treating the extracted host as deployment
qualified. Fresh full-suite and scenario-input hosted qualification remain
outstanding. Positive receiver qualification must use an explicitly approved
caller; the default policy intentionally rejects every caller.

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
