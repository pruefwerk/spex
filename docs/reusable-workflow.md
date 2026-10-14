# Prepared requests in reusable workflows

Remote dispatch and reusable workflows consume the same `spex.transport/v1`
request. The Action prepares authoring data; Spex authenticates the source,
resolves inherited configuration, plans and executes tests, and writes evidence.

## Prepare the request

Use spex-action with `execution-mode: prepare`. It writes and uploads the request
without transport credentials, a receiver destination or a local Spex install.
Pass `request-id`, `request-sha256` and `request-artifact-id` as job outputs to
the runtime repository's reusable workflow. The preparation step does not run
acceptance tests, so its success does not establish their outcome.

The reusable workflow runs in the caller's GitHub context. It consumes artifacts
from that same repository and run with `GITHUB_TOKEN`. It needs `actions: read`
and `contents: write`: the latter creates durable `spex-request-*` replay claims
in the caller repository. Supply `id-token: write` if the runtime uses OIDC.
The caller must grant these permissions; a called workflow cannot elevate them.

## Stage trusted runtime files

A private reusable workflow does not grant the caller token arbitrary repository
checkout access. Supply trusted catalogs, bindings, profiles and fixtures through
a shared Action in the runtime repository. Enable GitHub Actions sharing
for the intended repositories and pin that Action and the reusable workflow to
the same reviewed release.

After installing the runtime-owned Spex release, the shared Action calls:

```sh
spex runtime stage --source "$GITHUB_ACTION_PATH/../../.." --config spex-runtime.toml
```

Spex copies the trusted files to a private execution directory under `RUNNER_TEMP`.
It skips `.git`, `.github`, `.spex`, `.secrets` and `artifacts`, rejects symlinks
and non-regular files, and bounds file count and size. The selected definition
becomes the staged `spex-runtime.toml`, so existing thin command aliases keep
working. The downloaded shared Action directory remains unchanged.

The command writes `runtime-directory`, `runtime-config` and `runtime-release`
through `GITHUB_OUTPUT`. The release is a deterministic content digest of the
staged files and their executable permissions. Concurrent requests receive
separate writable directories, even when they share a runner daemon.

## Admit and execute

Run from the staged runtime directory:

```sh
spex runtime --config spex-runtime.toml source-admit-reusable
```

Supply `REQUEST_ID`, `REQUEST_SHA256`, `SOURCE_ARTIFACT_ID` and `GITHUB_TOKEN`.
Spex derives the source repository and run ID from the GitHub context. It ignores
cross-repository source selectors and verifies the current source commit, event
and actor against authenticated run metadata. Push and manual-dispatch runs on
their first attempt are supported; pull requests, forks and reruns fail closed.
The configured source policy still controls organization, visibility and any
additional repository, workflow, branch or actor restrictions.

The same admission code validates the artifact's name, run, source commit, expiry
and size, fetches the pinned source, and writes private admission and scope files.
No test environment starts during admission. The reusable path records the
staged bundle identity as the runtime release; its receipt's workflow SHA names
the caller workflow commit. Remote dispatch keeps its existing receiver-commit
identity.

Execute the returned `context-path` through the existing host command:

```sh
spex runtime --config spex-runtime.toml "$ADMISSION_CONTEXT"
```

Use the caller token as `RECEIVER_CLAIM_TOKEN`. Both invocation paths use the same
guardrails, typed overlays, optional scheduler, private Kind lifecycle and cleanup.
Omit explicit tests to discover the runtime's existing suite; enable trusted
`caller_tests` policy to accept inline or file-based caller tests.

Upload only the host's receipt, safe evidence directory and ownership ledgers.
Keep downloaded source, registry credentials and admission contexts private.
Call `receiver-cleanup` for bounded recovery on failure or cancellation, and
preserve the execution failure even if artifact collection or cleanup also fails.

## Qualification

Local tests cover prepare-only packaging, current-run admission, private staging,
stable bundle identities, cancellation and unchanged host execution. A hosted
caller-to-reusable-workflow run still needs separate qualification. Before
activation, publish and pin a release containing these commands.
