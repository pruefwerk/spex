# Compatibility Policy

Current schema family: `v0.1`.

## Versioned Inputs

- `spex.scenario.v0.1`
- `spex.suite.v0.1`
- `spex.binding.v0.1`
- `spex.integration.v0.1`
- `spex.catalog.v0.1`

Unknown fields are rejected. Trailing YAML documents are rejected. Inputs are expected to be regular files with bounded size.

## Compatibility Promise Before v1

Before `v1`, releases may still refine validation and generated manifests. A change is acceptable only when:

- `make verify` passes.
- `make production-candidate-check` passes.
- reference scenario repo checks pass.
- at least one live proof passes before promotion if generated runtime behavior changed.

## Breaking Changes

Treat these as breaking changes:

- removing a schema field
- changing the meaning of a schema field
- changing default operation ordering
- changing report schema fields
- changing generated labels used for evidence mapping
- changing secret materialization behavior
- changing external Git ref semantics

Breaking changes require:

- changelog entry
- migration note
- schema version decision
- live proof

## Deprecation

Do not remove a documented field in the same release where it is deprecated. Mark it as deprecated in docs and schema descriptions first, then remove it in a later schema version.

## Generated Workspace Compatibility

Generated KUTTL workspaces are build artifacts. They are inspectable and retained for evidence, but the compatibility contract is the input schemas, CLI behavior, and report schemas, not hand-edited generated files.
