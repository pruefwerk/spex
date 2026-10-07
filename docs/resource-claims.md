# Coordinating resource access

Spex coordinates participating executions. The runtime identifies resources and
decides which operations can safely overlap. Resource claims grant access, not
ownership: they never authorize deleting an existing gateway, database or topic.

## Runtime responsibilities

A runtime declares its complete claim set in `PlanSummary.ResourceClaims` before
execution. Each claim names a physical resource and requests `shared` or
`exclusive` access. Shared readers can overlap; an exclusive claim conflicts with
every other claim on that resource. Spex acquires the set atomically, so a waiting
execution never holds some resources while waiting for others.

The runtime must choose stable, non-secret identities. For example,
`aws-dev/gateway/0200000100009001` identifies the same gateway across workflows.
A Kind identity must include the unique cluster identity: two private clusters
do not share their Redpanda instance merely because they use the same topic name.
Conversely, event IDs distinguish evidence but do not protect concurrent edits
to the same AWS gateway. Claim any additional shared mutation targets as needed.

Resolve and Plan must not mutate the environment. Include all required claims in
the plan; Spex rejects changes to that set after preparation. There is no generic
resource topology in scenario TOML. Runtime configuration remains runtime-owned.

## Host configuration

The trusted host supplies a coordinator:

```go
registry := scenarioruntime.NewRegistry(
    scenarioruntime.WithResourceClaims(&resourceclaims.Coordinator{Store: store}),
)
```

A receiver supplies the same coordinator through `receiver.Host.ResourceCoordinator`.
Submitted definitions cannot choose the coordination backend or namespace. A plan
with claims fails preparation if the host has not configured a coordinator.
Existing runtimes without claims keep their existing execution path; they do not
gain protection until they declare their resources.

The migration-testbench factory also exposes `WithResourceContract(base, contract,
verifier)`. Its trusted contract binds claims to a canonical scenario ID and exact
test plan. The wrapper keeps legacy configuration/execution intact and asks the
runtime host's verifier to check safe reuse. A missing verifier cannot release
shared claims. The local `scenario validate/explain/run --resource-contract` bridge
supports resource-free private stacks; shared claims require a receiver host with
a coordinator. Backend credentials and the verifier are not scenario inputs.

Available stores:

| Store | Coordination scope |
| --- | --- |
| `MemoryStore` | One process sharing the same instance; unit tests only for distributed use. |
| `NewFileStore(directory)` | Processes on one Linux/macOS host using the same durable local directory. Not NFS or separate ephemeral runners. |
| `NewMongoStore(collection, namespace)` | Independent runners using the same MongoDB collection and coordination namespace. |

Use a dedicated MongoDB collection with host-owned credentials and primary,
majority reads/writes. The adapter applies those concerns itself. All executions
that can touch the same resources must use the same coordination namespace.
Do not split it by workflow run or scenario ID. The store never installs TTL
indexes; administrators must not add expiration or delete active claim records.

The Store contract requires atomic transactions. Callbacks may run more than
once and must perform no I/O or other external side effects. Backend errors fail
closed and do not expose connection strings or credentials.

## Release and recovery

After execution and bounded cleanup, the runtime must explicitly set
`ExecutionResult.ResourceClaimsSafeToRelease` when it has verified safe access
for the next execution. Successful cleanup of temporary probes alone does not
establish that application state or in-flight commands are safe. Spex releases
claims only with that attestation and cleanup `succeeded` or `not_run`.

Without the attestation, Spex retains the holding as `recovery_required`.
If a worker exits abruptly, a durable store retains its active holding. Neither
elapsed time nor cancellation expires it. Even a shared holding awaiting recovery
blocks new readers until reconciliation.

A trusted host can call `Coordinator.Inspect`, identify the worker, verify that
it has stopped, and check the runtime's resource postconditions. Only then may it
call `Recover` with both checks confirmed and an evidence reference. The host
must retain the returned holding and its recovery evidence in its audit trail;
the library does not investigate workers or persist that audit itself. Do not
release an apparently abandoned holding while its worker may still execute.

## Evidence and limits

`plan.json` records normalized claims. With an artifact sink, Spex writes
`resource-claims.json` before environment mutation so the host can identify an
abandoned holding even without a final result. A failed acquisition-evidence write
prevents execution. Hosts using `Prepared.Execute` without a sink must retain that
correlation themselves. `result.json` records the acquisition owner,
wait duration, requested claims and final disposition: `released`,
`recovery_required`, `not_acquired`, or `unknown` when the backend outcome is
uncertain. Treat `unknown` as requiring inspection, not permission to retry writes.
Secondary claim problems do not erase a failed or cancelled primary outcome.
A passing test with unresolved claims becomes an execution error.

Resource keys appear in evidence: runtimes must not use credentials or secret
connection strings as identities. Request admission/replay protection remains
separate from resource claims. Global runner capacity and workflow queuing also
remain separate. Waiting is cancellable polling, not a FIFO queue; fairness and
starvation prevention are not guaranteed. Claims coordinate only participants:
operators and other writers must honor the same boundaries.

Run local tests with `go test -race ./pkg/resourceclaims ./pkg/scenarioruntime`.
Set `SPEX_CLAIMS_MONGODB_URI` to a disposable MongoDB test server to also exercise
two independent clients, conflicting access, recovery and concurrent writes.
That test creates and drops only its uniquely named test database.
