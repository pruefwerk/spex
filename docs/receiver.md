# Runtime-owned receivers

A receiver is a GitHub Actions workflow in the runtime repository. For example,
migration-testbench owns the workflow that deploys its test environment. Spex
provides a Go API and an [example workflow](../examples/receiver/workflow.yaml),
not a hosted acceptance service or a production receiver workflow.

The [entrypoint walkthrough](../examples/receiver/entrypoints.md) illustrates
authentication and fetching, tooling setup, Go execution, policy and cleanup.

## Responsibilities

The action uploads a `spex.transport/v1` request and dispatches the selected
workflow. The receiver owns its Spex version, runtime releases, credentials,
runner, infrastructure and artifact storage.

The host must authenticate the source repository, run and artifact through GitHub,
authorize the caller, verify the source commit and fetch that exact commit.
Dispatch inputs alone do not authorize execution. Keep submitted source separate
from trusted runtime configuration and never execute its setup scripts.

For shared mutation targets, the host also supplies
`Host.ResourceCoordinator` and the runtime declares physical resource claims in
its execution plan. Spex acquires them before execution and retains unsafe or
abandoned holdings for verified recovery. Request replay protection does not
replace this coordination. See [resource claims](resource-claims.md) for store
scope, runtime responsibilities and recovery.

`receiver.DecodeArtifact` checks the bounded, single-file archive, exact-byte
digest, schema and source identity against host-verified metadata. It does not
contact GitHub. `Checkout` attests what the host fetched; the library does not
independently prove that a directory contains that commit.

## Execution

Pass the decoded request, attested checkout, runtime catalog, mandatory policy
and artifact sink to `receiver.Execute`. It performs these steps:

1. Admit the caller and durably claim the request ID.
2. Load definitions from the confined source root.
3. Select a supported runtime release and canonicalize the scenario.
4. Check sources and admit the resolved scenario.
5. Resolve inherited runtime configuration, plan and execute through
   `scenarioruntime.Run`.
6. Write the transport receipt alongside existing execution evidence.

`Policy.Admit`, `Policy.Claim` and `Policy.Allow` have no permissive defaults.
The host supplies authenticated actor information to its policy. Claims must
survive concurrent workflows and reject replays. `Allow` must restrict operations
and configuration before runtime resolution. Parsing does not authorize operations.

The CLI and receiver share `pkg/definition` for inline TOML, YAML/Gherkin documents
and file patterns. Configuration comes from definition TOML; the transport accepts
no separate runtime-config overlay.

`pkg/migrationtestbench.New` exposes the existing adapter with trusted repository,
suite and release configuration. It retains the resolver, executor and cleanup
behavior. The host must bind the release label to the actual pinned runtime
checkout; the factory does not download or verify a release.

## Results and cancellation

Supply the workflow SHA, run ID, attempt and actual Spex version. The receiver
writes `receipt.json` using `spex.transport-result/v1`, including the canonical
scenario identity, runtime release and execution result. Early rejection can omit
unresolved identity/version fields. Success requires at least one planned test
and a complete passing result population.

Upload only `receipt.json` as `spex-result-<request_id>`. Publish canonical scenario
and redacted evidence separately. Propagate `receiver.ExitCode` after uploads:
reporting failures must not erase failed tests. The action also checks the workflow
conclusion, so host cleanup failures cannot become successful acceptance.

Pass a signal-aware context. The existing runtime handles bounded cleanup; bound
cleanup of additional host resources too. Forced runner termination can prevent
receipt upload. Missing evidence never establishes success.

Never submit expanded credentials. Use supported runtime secret references and
redacted evidence. Do not upload raw admission metadata, tokens, source archives
or arbitrary receiver directories as evidence.

## Qualification boundary

Tests cover admission data, lifecycle ordering, duplicate claims, sources,
cancellation, failure preservation and receipt persistence. A contract test uses
the real action request builder and receipt validator with a fixture runtime.
Another exercises the migration-testbench adapter with a simulated Kubernetes
runner. Neither qualifies GitHub authentication, a durable claim store, live
deployments or the production migration-testbench receiver workflow.
