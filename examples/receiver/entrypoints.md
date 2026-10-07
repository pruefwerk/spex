# Four receiver entrypoints, illustrated

These examples show what each entrypoint does and what it passes to the next
step. They are implementation sketches, not a second receiver implementation.
Names such as `github`, `policy` and `claims` below represent adapters supplied
by the runtime owner; they are not Spex APIs. Spex API names are qualified.

Keep three directories separate:

```text
trusted/                 pinned runtime checkout and compiled host
source/                  authenticated caller commit; treat as untrusted data
private/                 admission context and downloaded request
evidence/                canonical scenario, results and redacted artifacts
```

The host chooses these paths. Do not take them from submitted definitions. Keep
private files outside the evidence directory, with owner-only permissions.

## 1. ci/receiver-admit-and-fetch

This step turns dispatch inputs into verified source metadata. For example, a
receiver can restrict callers to selected repositories and workflow identities,
and reject fork pull requests. The runtime owner chooses that policy.

Illustrative control flow (pseudocode):

```text
require transport_schema == "spex.transport/v1"
validate request_id, request_sha256, repository and numeric IDs

run = github.getRun(source_repository, source_run_id)
policy.requireAllowedSource(run.repository, run.workflow, run.actor, run.event)
policy.requireAllowedCommit(run.head_sha, run.head_repository, run.ref)

artifact = github.getRunArtifact(run, source_artifact_id)
require artifact belongs to this exact run and run attempt
require artifact.name == "spex-request-" + request_id
require artifact is not expired and within download size limit

archive = github.downloadArtifact(artifact)  # bounded download
expected = receiver.Expected{
    RequestID: request_id,
    SHA256: request_sha256,
    Repository: run.repository,
    Commit: run.head_sha,
    SourceRunID: run.id,
}
request = receiver.DecodeArtifact(archive, expected)

checkout = github.fetchExactCommit(run.repository, run.head_sha, sourceDirectory)
require checkout commit == expected.Commit
writePrivateContext(expected, checkout, archivePath, authenticatedActor)
publishStepOutput("context-path", privateContextPath)
```

Fetch source without running its hooks, build scripts, submodules or setup steps.
Use bounded downloads and confined extraction if fetching an archive. Do not
persist the GitHub token in the checkout or admission context. The run may still
be in progress because the caller is waiting for this receiver; do not require
the entire caller workflow to have finished.

The private context contains verified identities and host-chosen paths, not
credentials. It belongs to this receiver execution; do not accept a context file
from the caller. Runtime admission must still check the resolved scenario later.

## 2. ci/receiver-setup

Install or build the receiver from the trusted checkout, not the caller checkout.
For a runtime repository with a committed Go module and pinned dependencies:

```sh
#!/bin/sh
set -eu
# GitHub starts this step in the trusted runtime checkout.
# cmd/receiver-host belongs to that repository, not to Spex.
go mod verify
go build -mod=readonly -trimpath -o "$RUNNER_TEMP/receiver-host" ./cmd/receiver-host
```

Provision the required Go toolchain before this script. The committed module
pins the Spex library version. Install other tools from the runtime's existing
version/checksum manifest rather than duplicating pins here.

This step must not create a cluster or deploy services. The selected runtime
performs environment setup only after definition validation, policy checks and
planning. It also owns credentials and any renewal hooks needed during execution.

## 3. ci/receiver-run

A small shell entrypoint can hand control to the compiled host:

```sh
#!/bin/sh
set -eu
exec "$RUNNER_TEMP/receiver-host" run --context "$ADMISSION_CONTEXT"
```

The host reads its private context and constructs the following objects. This Go
sketch omits error handling for readability; production code must stop on every
error before continuing. It is not a complete `main` function.

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()

runtime, err := migrationtestbench.New(migrationtestbench.Config{
    RepositoryRoot: trustedRuntimeRoot,
    SuitePath:     trustedSuitePath,
    Release:       pinnedRuntimeRelease,
})
// Check err before continuing.
host := receiver.Host{
    Policy:         admissionPolicy,
    DefaultRuntime: runtime.ID(),
    Runtimes:       []receiver.Choice{{Runtime: runtime, Default: true}},
    Execution: receiver.Execution{
        WorkflowSHA: workflowSHA,
        RunID:       receiverRunID,
        RunAttempt:  receiverRunAttempt,
        SpexVersion: compiledSpexVersion,
    },
}
request, err := receiver.DecodeArtifact(archive, verifiedExpected)
// Check err before continuing.
sink, err := scenarioruntime.NewFileArtifacts(hostWorkspace, "evidence", request.SHA256())
// Check err before continuing; close sink before exiting the process.
receipt, runErr := receiver.Execute(ctx, request, verifiedCheckout, host, sink)
code := receiver.ExitCode(receipt, runErr)
```

Read workflow/run identity from the receiver's own GitHub context. Use the actual
compiled Spex version and a release label tied to the trusted runtime checkout,
not values from the request. The sink groups files by request digest; the final
canonical scenario ID remains in the receipt and execution result.

The mandatory `admissionPolicy` can follow this pattern (pseudocode):

```text
Admit(envelope):
    compare envelope source with private authenticated context
    check source/actor policy again

Claim(requestID):
    atomically insert (receiver identity, requestID) into durable claims store
    reject if already present; never replace an existing claim
    reject on store failure; do not silently run without replay protection

Allow(canonicalScenario):
    require an allowed runtime and release
    restrict submitted tests, operations and typed configuration
    reject caller-selected hooks, credentials or unrestricted deployment targets
```

A local file on an ephemeral runner is not a durable claims store. GitHub
concurrency groups can serialize runs but do not by themselves prevent replay.
After rejection or failure, a fresh invocation creates a new request ID; do not
delete claims to make an old request executable again.

After execution, publish `receipt-path` only if `receipt.json` exists. Publish
`evidence-directory` for the sink's directory. Use a GitHub-output writer that
rejects line breaks in paths; never interpolate these values into shell source.
Attempt output publication even after failed tests, then return `code`. If
publishing outputs fails after a passing test, fail the host instead of claiming
complete success. Preserve an existing test-failure or cancellation exit code.

## 4. ci/receiver-cleanup

Runtime cleanup already runs through Spex. This final entrypoint handles only
additional host-owned resources, if any. A receiver with no additional resources
can use this complete script:

```sh
#!/bin/sh
set -eu
# The runtime owns environment teardown. No extra host resources exist.
exit 0
```

For a host that allocates extra infrastructure, use an ownership ledger instead
of broad deletion (pseudocode):

```text
if no host-resource ledger exists: return success
for resource in this execution's private ledger:
    verify resource ownership matches receiver run ID and request ID
    delete that exact resource with a bounded timeout
    treat already absent as success
retain evidence and report any cleanup failure
```

Record resources as they are created so partial setup remains recoverable.
Never derive deletion targets from submitted configuration alone. Keep the
workflow's cleanup timeout and do not let cleanup replace the primary test
failure. The example workflow uploads evidence first and then preserves both
execution failure and any failed cleanup step in its conclusion.
