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
