# Implementation progress

## Phase 0

Inventory and baseline recorded. Local gates pass. Fresh live acceptance remains
blocked by the environment described in `blockers.json`.

## Phase 1

`pkg/scenario` provides the model, strict TOML parser, canonical serialization,
full SHA-256 identity, source confinement and authoring builder. Runtime config
remains opaque canonical TOML; merging requires a runtime-owned typed merger.
Source paths resolve against the caller's workspace, not the TOML file's directory.
Canonicalization does not resolve secret references. Runtime validation must
complete before any execution description is persisted.

Validation: 12 parser/model tests; 509 Spex race tests across 9 packages; `go vet`;
existing security and production-candidate gates all passed. No legacy CLI
dispatch or execution path changed. Live tests retain the Phase 0 blocker.

## Phase 2

`pkg/scenarioruntime` defines the in-process registry and resolve/plan/execute
contract. The registry validates sources before resolution and prevents execution
after validation, resolution or planning failure. Runtime-owned plans expose only
a safe summary. Results retain a primary test failure alongside cleanup problems.
Error wrappers omit underlying configuration text while preserving cancellation
inspection through `errors.Is`.

Validation: 518 Spex race tests across 10 packages; `go vet`; security and
production-candidate gates; all 122 testbench harness tests passed. Runtime tests
exercise ordering, cancellation, runtime selection and primary-failure retention.
No production runtime or new CLI command is wired yet. Live tests remain blocked.

## Phase 3 — local adapter; live parity blocked

The legacy `suite run` entrypoint and the in-process migration runtime now share
`runResolvedSuite`. The adapter calls `LoadScenarioSuite` and `loadSuiteInputs`;
it does not reproduce the testbench's configuration model. Empty-overlay tests
compare resolved configurations and discovery directly. A second test compares
JUnit evidence from the legacy and shared execution paths.

Context-aware variants propagate cancellation through hooks, KUTTL, evidence
commands and rate limiting. Unix commands run in isolated process groups when
cancellable. Runtime cleanup gets a separate 30-second budget; legacy callers
retain their existing execution interface. The adapter distinguishes mapped
operation failures from setup, runner and missing-evidence failures.

At the Phase 3 checkpoint, the internal adapter accepted only inherited suite
configuration. Phase 4 extends that surface below. Do not use these checkpoints
as a release candidate for the completed scenario product.

Local qualification: 529 Spex race tests, `go vet`, all 122 testbench harness
tests, all testbench suite validators (including 73 Gateway Migration scenarios),
catalog validation, and the security/production-candidate gates passed. Fresh
full acceptance parity remains blocked by the Phase 0 infrastructure constraints.

## Phase 4 — first typed overlay surface

The runtime decodes strict TOML into optional typed fields and applies them after
the existing resolver finishes. Supported fields currently cover suite selection,
environment namespace/context, probe image/pull policy, and execution controls
(fail-fast, repetitions, concurrency, maximum failures, resource retention and
resource-usage collection). Suite selection chooses an existing configuration;
it does not create a second configuration model. Unknown fields fail validation.

The pure application operation preserves inherited values and input storage.
Authoring merges use explicit typed fields, not recursive maps or reflection.
Tests cover absent values, nested overrides, explicit false/zero, legal empty
context, invalid values, unknown keys and secret-safe error text. Scenario
execution also propagates its metadata timeout to the runtime.

This is a deliberately limited surface: no map/array override fields or service
image overrides are exposed yet. Their semantics and tests remain outstanding;
this checkpoint does not satisfy the entire Phase 4 acceptance matrix. No new
CLI or Action entrypoint exposes the adapter yet. Local qualification passed:
533 Spex race tests, `go vet`, 122 testbench harness tests, suite/catalog
validation, and the existing security and production-candidate checks. Live
qualification retains the recorded infrastructure blocker.

## Remaining campaign work

- Complete the typed overlay surface against actual scenario requirements.
- Qualify scenario run against the live migration environment.
- Persist canonical artifacts and versioned results; qualify redaction and all
  primary/secondary failure paths before exposing execution publicly.
- Qualify and publish the local `pruefwerk/spex-action` implementation after the
  scenario-capable Spex release and hosted/live gates pass.
- Qualify inline, file and committed-scenario authoring and local reproduction.
- Adapt migration-testbench CI and portable environment lifecycle without
  duplicating its existing configuration or deployment machinery.
- Write usage documentation and exactly three primary examples.
- Run the full release matrix and produce `FINAL_REPORT.md`. Live acceptance
  requires the authorized environment recorded in `blockers.json`.

The migration-testbench dependency pin and production CI remain unchanged. No
release or remote publication has been made during this campaign.

## Phase 5 — source handling and authoring CLI preview

Explicit file sources and inline sources now use the same YAML/Gherkin parsers,
catalog expansion, binding validation and shared suite executor. Inline validation
does not materialize files. Existing explicit file selections preserve matching
suite-level per-source parameters, tags, binding and profile overrides. New
sources inherit the suite's base binding and profile. Logical inline errors and
plan summaries identify `scenario.toml:test[n]`.

The CLI now exposes build, validate and explain. The builder accepts source files
and TOML fragments as files, preserves explicit authoring precedence and writes
only after runtime validation. Canonical output uses confined filesystem access,
private file permissions and exclusive creation; it cannot overwrite an existing
file or follow an output symlink outside the workspace.

`scenario run` remains deliberately unavailable until execution artifact safety
is qualified. The preview is documented in `docs/scenario-preview.md`. Local
qualification passed: 539 Spex race tests, `go vet`, all 122 testbench harness
tests, suite/catalog validation, and existing security/production-candidate
checks. This is not completion of Phase 5 or of the campaign. Live acceptance
retains the recorded infrastructure blocker.

## Phase 6 — run artifacts and failure preservation

`scenario run` now executes the prepared runtime and writes a canonical scenario,
plan, runtime identity, versioned result and typed step-outcome evidence. Artifact
writes use a confined filesystem root and exclusive creation. Reruns use separate
execution directories beneath the unchanged semantic scenario hash.

The migration adapter runs existing tools in a private temporary workspace. It
exports only a typed evidence projection, not raw logs, manifests, kubeconfig or
resolved bindings. Normal completion and cancellation remove that workspace;
forced termination or host failure can leave transient files behind. This is
an artifact export boundary, not a claim that underlying tools never write
sensitive transient files. Detailed backend diagnostics remain intentionally
excluded until a stronger runtime-specific redaction contract is qualified.

Reporting failures retain a primary failure. Reports now record cleanup failure
separately instead of replacing the original failure class. CLI cancellation
propagates to execution and returns 130; mapped test failures return 3 and
infrastructure/artifact failures return 4. Existing legacy entrypoints remain.

New local tests exercise reruns, artifact confinement, sentinel secrets from
environment/backend output, cancellation results, preparation failures and
primary/secondary failure ordering. Local qualification passed: 547 Spex race
tests, `go vet`, 122 testbench harness tests, suite/catalog validation, and the
existing security and production-candidate checks.
Live qualification, the Action, long-running credential renewal through the new
entrypoint, and testbench CI adoption remain outstanding.

## Phase 7 — local composite Action

The new `pruefwerk/spex-action` repository now contains the composite metadata,
Python transport/bootstrap, tests, documentation and a qualification workflow.
It delegates scenario construction and execution to the same CLI used locally.
Inline source and runtime configuration travel through private input files;
other values use argv. The wrapper neither parses TOML nor merges runtime fields.

Users select an explicit executable or published Spex version. Installation
reuses the existing release archive and requires its checksum. The wrapper
exports paths/identity, forwards cancellation, and preserves a primary exit code
when output writing or temporary-file cleanup also fails. Artifact upload stays
with the caller. Exactly three primary authoring examples are documented.

Local qualification passed: 17 adapter/installer unit tests plus seven tests
against the candidate Spex binary with simulated KUTTL, Python lint/format and
workflow lint. These tests include all authoring modes, local canonical replay,
runtime discovery, failure outputs and SIGTERM propagation. Existing Spex and
testbench local regression gates also passed. The Action qualification document
records the precise scope and remaining blockers.

The GitHub workflow has not run, no remote repository/tag has been published,
and no scenario-capable release is available for live installer qualification.
Live migration acceptance, portable testbench lifecycle and CI adoption remain
outstanding. This is not completion of Phases 8–12 or of the campaign.

## Testbench adoption preparation — selection and renewal

Typed selection arrays now delegate to the existing suite tag filters. Authoring
overrides replace arrays, including explicit empty arrays. The runtime also
exposes the existing per-scenario executable hook and bounded timeout. It checks
workspace confinement and executability during preparation, but invokes hooks
only during execution. Tests cover repeated renewal, absence of global environment
mutation and credential sentinels in retained artifacts.

The testbench has a minimal committed scenario and an opt-in authoring script.
The script reuses existing group validation, delegates canonical construction to
Spex and replaces itself with the CLI for cancellation propagation. Production
CI and the dependency pin remain unchanged. Existing profiles still require the
testbench working directory; the adapter preserves that legacy convention.

Live qualification remains blocked. The production report collector and shared
cluster lifecycle must be integrated before the scenario path can replace the
current workflow. Local qualification passed: 551 Go race tests, `go vet`,
security and production-candidate checks, 127 testbench harness tests, all suite
and catalog validators, and 25 Action tests against the candidate binary.
Read-only scenario validation covers all 73 Gateway Migration scenarios; selecting
verification, lifecycle and Modbus produces 31 tests. The Action also passes
Pyright and Ruff. These results do not establish live acceptance parity.

## Hosted qualification wiring — opt-in, not yet exercised live

`reports scenario` reads canonical runtime artifacts without importing testbench
semantics. It checks scenario identity, result schema and completion counts, and
refuses historical multi-run directories. Its output contains only identifiers,
enumerated outcomes and counts. Tests also check artifacts from an actual local
scenario execution with simulated KUTTL.

The testbench now reuses its prepare/run/cleanup lifecycle for opt-in scenario
groups. Each invocation has an isolated safe artifact directory. Report failures
fail successful executions but preserve test failures, cancellation and timeouts.
Raw cluster diagnostics remain excluded from this path. A manual workflow input
selects an explicit published candidate; an empty input preserves rc.37 and the
legacy path. Local orchestration tests exercise reuse, cleanup and failure codes
with fake external tools. Hosted execution and live parity remain BLOCKED until
a candidate is available in the authorized GitHub environment.

Local gates passed: 561 Go race tests, `go vet`, security/release checks, 133
testbench harness tests, all suite/catalog validators and 25 Action tests.
Workflow lint passes with the existing enterprise runner label explicitly
allowed. Canonical baseline and Modbus group preparation also validate against
the actual testbench configuration without running hooks or deployments.

## rc.38 publication and hosted qualification

Published `v0.1.0-rc.38` from `3a02c430f68327c9d2f36976df6483b973eb0b1c`.
The hosted release workflow passed security, production-candidate, archive and
HTTP bundle gates: https://github.com/pruefwerk/spex/actions/runs/37523865313.
The release remains a prerelease, not a claim of full scenario-runtime parity.

Dispatched all Gateway Migration groups through the opt-in scenario path on
testbench commit `c932888607948093267c15e67cae181b10cb591b`:
https://github.com/VFCOM-DigitalEngineering-ST-GK/migration-testbench/actions/runs/37524238938.
Initial group selection passed; acceptance results remain pending. The ordinary
testbench workflow still defaults to rc.37 and the legacy path. The Action has
not been published by this checkpoint.

## Architecture revision: remote submission client

The user selected remote workflow execution to decouple the caller's build image,
tools and network from acceptance infrastructure. The in-process runtime remains
inside the receiver. A Spex client now packages inputs and implements GitHub
dispatch/correlation/wait/cancellation/result verification; spex-action owns only
input/output transport and GitHub artifact upload. Local execution remains explicit.

This supersedes automatic runtime checkout in the Action. The new package/submit
commands are not in rc.38. The receiver contract, admission policy, result envelope
and remaining limitations are documented. Mock API tests do not establish live
remote execution; implementing and qualifying the testbench receiver is required
before release. The previous hosted rc.38 run qualifies a different, local-CLI path.

Local qualification: 576 Go race tests, 31 Action tests, Python lint/type checks,
workflow lint, Go vet and the existing security/release gates passed. All 133
testbench harness tests and its suite/catalog validators passed with the candidate
client. Receipt tests reject incomplete success and keep misleading receiver
success out of retained results. No remote workflow was dispatched and no release
was published for this architecture revision.

## Runtime-owned receiver API

The Action now sends opaque authoring requests without installing Spex. Public
`pkg/receiver` validates the transport, invokes shared `pkg/definition` authoring
and runtime selection, and executes through `scenarioruntime.Run`. The CLI shares
the extracted definition loader. `pkg/migrationtestbench` exposes the existing
adapter with a trusted suite and release pin.

Spex supplies only an example workflow under `examples/receiver`. Production
workflows belong to runtime repositories. Authenticated source fetching, durable
request claims and deployment policy remain explicit host responsibilities.
See [the receiver contract](../../receiver.md).

Contract tests exercise the real Action builder and receipt validator. Adapter
tests use a simulated Kubernetes runner. These checks do not establish live
GitHub execution or qualify the production migration-testbench receiver.

Local qualification on 2026-10-07: 620 Go race tests passed with the cross-repository
Action contract test enabled; 37 HTTP probe tests and 70 Action tests passed.
Go vet, module verification, example workflow lint and whitespace checks passed.
No production receiver was deployed, no remote workflow was dispatched and no
release was published.

## Resource coordination mechanism

Spex now supplies `pkg/resourceclaims`: atomic shared/exclusive claim sets,
cancellable conflict waits, explicit safe release and verified recovery. Memory,
durable local-file and shared MongoDB stores separate single-process tests,
same-host processes and independent runners. No adapter expires abandoned owners.

Runtime plans declare physical identities. The registry and receiver acquire
claims before environment mutation, persist acquisition evidence, and include
disposition in the execution result. Runtime attestation of safe reuse remains
separate from temporary-resource cleanup. Unresolved claims preserve primary
failures and prevent a passing test from becoming a successful execution.

See [resource claims](../../resource-claims.md) for the host contract, backend
scope, recovery and limitations. The migration-testbench runtime does not yet
declare its resources or configure the production coordinator. This mechanism
does not remove its existing execution blockers or establish AWS isolation.

Local qualification on 2026-10-07: 644 Go race tests passed, including the real
Action compatibility contract and a disposable MongoDB replica-set test with two
independent clients. The separate HTTP probe module passed 37 tests. Go vet,
module verification and whitespace checks passed. Package execution was serialized
for this full run after parallel package load exceeded an existing one-second
credential-hook fixture deadline. Process-exit tests verify retained local-file
claims; live MongoDB tests verify contention, recovery and concurrent updates.
The disposable MongoDB container and its test database were removed. No live AWS
or hosted receiver qualification was performed, and no release was published.
All 70 Action tests also passed against the just-built candidate executable.

## Migration-testbench resource contracts

The existing suite runtime now supports a trusted resource contract through
`migrationtestbench.WithResourceContract`. It checks canonical scenario identity
and the exact selected plan, preserves the resolver/executor and persists the
contract. Shared-resource release requires a host-owned safety verifier, separate
from temporary cleanup. The local CLI supports resource-free contracts; shared
claims still require a receiver coordinator and have no CLI bypass.

The testbench scenario runner resolves a contract before execution. Private Kind
stacks declare no shared-stack resources. Its AWS policy resolver produces stable
physical keys, normalizes EUIs and rejects uncontracted tests. Policies and backend
configuration remain trusted host inputs, not submitted definitions. Production
AWS execution remains blocked pending the receiver, target policies, safety
verifier, ownership ledger and three-part reporting.

Local regression qualification: 648 Go race tests passed (the optional live
MongoDB test was skipped in this run), all 70 Action tests passed against the
candidate, and Go vet/whitespace checks passed. The testbench's 73 Gateway Migration
scenarios and shared step catalog validated without deployment. No hosted workflow,
live migration or AWS run was executed. These APIs/CLI additions were unreleased
at that local qualification checkpoint.

## rc.39 publication

Published `v0.1.0-rc.39` from `0763f8ffcbf50713e6898b7bd09e9d8187a96b04`.
The [hosted release workflow](https://github.com/pruefwerk/spex/actions/runs/37642771860)
passed security, production-candidate, archive and HTTP bundle gates. The
[release](https://github.com/pruefwerk/spex/releases/tag/v0.1.0-rc.39) is a published
prerelease, not a claim of production receiver deployment or live AWS qualification.

Both downloadable archives passed checksum verification. The testbench HTTP bundle
installer consumed rc.39 successfully, and its workflow/local instructions now
pin that release. The default testbench execution remains the legacy suite path;
scenario-runtime qualification remains explicit. No testbench acceptance workflow
was dispatched. The success-only credential-hook fixture now allows ten seconds
for process startup; dedicated expiry tests and production timeouts are unchanged.

## Native testbench consolidation — 8 October 2026

The current working tree supplies native lifecycle, scheduling, exact Kind/image
ownership, checked Helm reuse and console capture APIs. Suite inspection calls the
existing loader and validator. Portable source transport/extraction and credential
renewal accept host-owned policy; image fingerprints bind explicit inputs without
absolute checkout paths or timestamps.

Migration-testbench now uses one Go host for suite YAML, scenario TOML, local,
split CI and receiver execution. Its production Python adapters and harness were
removed after native parity checks. Source allowlists, profiles, role policy,
service fixtures and chart/image pins remain in that repository.

Spex passed 705 tests, the full race suite, vet and `make verify`. The host passed
120 tests, race checks, vet, module verification and build. All 24 suite entrypoints
and the 42-step catalog validated, including 73 migration scenarios. Real Spex
projection, live unscheduled host admission and overlapping Kind isolation passed
separately. These checks do not establish private-chart or AWS acceptance.

rc.39 lacks the pending SDK packages. Local qualification links the repositories
in a Go workspace; standalone published-pin builds remain blocked. No new release,
tag, commit or pin update accompanied consolidation. Publish and update SDK/tool
pins together before hosted acceptance qualification.

## Configured executable host — 2026-10-08

Spex now supplies `spex runtime --config <host.toml>` through
`pkg/runtimehost/engine`. Strict typed host definitions select existing runtime
inputs and project policy. Migration-testbench no longer compiles a Go host;
its command aliases invoke Spex and its `runtime/` contains policy only.

The resolver, final scenario overlay, owned lifecycle and capacity mechanisms
remain shared. Prepared executions record a host-policy fingerprint and refuse
drift before execution. A bounded catalog matcher cache avoids repeated expression
compilation without changing capture semantics.

Qualification passed: 734 Spex race checks, vet, `make verify`, 107 historical
host parity checks, all 16 setup fixtures, project black-box checks, all 24 suite
validators and live overlapping Kind cancellation/survivor qualification.
Temporary parity code was removed. These checks do not qualify private charts,
published tooling or AWS. rc.39 remains the pin; publication and coordinated tool
and qualification dependency updates are still required.

## Repository quality review — 2026-10-08

Host configuration, scope resolution and release version now belong to immutable
host instances. Compatibility bindings return defensive copies. The inherited
resolver's environment boundary remains serialized, and every host operation
restores its environment on success, failure and cancellation. Regression tests
cover independent hosts, stable scope names and environment restoration.

The executable has one command composition entrypoint. Tests invoke the built
binary for runtime selection, validation and commit-tag/workflow-dispatch inputs;
release archive qualification invokes the packaged binary as well. CI and release
workflows require race tests and vet. Manual release builds record the commit of
the checked-out tag rather than the workflow's original commit, with a regression
test that deliberately makes those commits differ.

The root dependency update to `golang.org/x/text v0.41.0` removes the reachable
GO-2026-6629 finding. Root and HTTP-probe security scans, race tests, vet and
migration-testbench compatibility checks passed. `make production-candidate-check`
passed, including archive verification and packaged-binary tests. All 24 testbench
suite entrypoints and the 42-step catalog validated with the updated executable.
The live shared-daemon qualifier passed cancellation and survivor-isolation
checks. These changes have not been published and do not establish private-chart
or AWS acceptance.

## Publication qualification — 2026-10-08

The configured host was committed as `ff01f77` and tagged rc.40. Local security,
race, static and production/archive checks passed, as did 70 Action adapter tests.
Standalone testbench qualification and live overlapping Kind cancellation passed
against the downloaded rc.40 module with `GOWORK=off`.

GitHub rejected rc.40 before creating a release. A unit fixture wrote temporary
scope values to the enclosing job's `GITHUB_ENV`, contaminating the next test
invocation. Fixtures now isolate GitHub command files. Cancellation could also
arrive between the scheduler's context check and admission transaction; confirmed
queued entries now withdraw under a fresh cleanup context, while uncertain running
slots remain reserved. Deterministic regressions and 20 repeated race-test runs
passed. The replacement candidate is rc.41; rc.40's tag remains unchanged.

The [qualification report](FINAL_REPORT.md) distinguishes local checks from
private-chart acceptance and positive receiver execution. The receiver allowlist
remains empty until a caller repository and ref receive explicit approval.

rc.41 is published from `b5e7142`. Release run `37837425065` passed, and downloaded
archives passed checksum and commit-provenance checks. Testbench pins now consume
rc.41 without workspace replacement. Standalone race/static checks, all suite and
catalog validators, 70 Action tests and live shared-daemon isolation passed.

The enterprise runner disables CGO by default. Testbench now explicitly enables
CGO for race tests and prepares a compiler in CI. A separate fixture scan found a
reachable `x/text` vulnerability; updated fixture dependencies and its builder
passed 160 race tests, static analysis, Linux compilation and a fresh scan with no
reachable findings. The hosted ownership group is pending on testbench `f256c0e`;
this does not yet qualify the complete private-chart suite or receiver transport.

Testbench `d75acbb` makes project fixtures use the pinned SDK's HTTP bundle rather
than an ignored checkout download. All 44 project race checks passed against a
clean Git snapshot. Hosted runtime/security and ownership checks restarted on
that commit; their completion remains a separate gate.

Hosted runtime run `37839862617` passed on `d75acbb`, including fixture race and
security checks. Ownership run `37839862440` passed selection and validation steps;
acceptance completion remains pending. Receiver admission remains deny-by-default.
