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
- Finish scenario run after qualifying secret-safe execution artifacts.
- Persist canonical artifacts and versioned results; qualify redaction and all
  primary/secondary failure paths before exposing execution publicly.
- Implement the thin Action in the initialized `pruefwerk/spex-action` repository.
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
