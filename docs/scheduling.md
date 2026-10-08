# Execution scheduling

Scheduling limits execution capacity. Resource claims coordinate access to
application resources. Neither mechanism establishes deletion ownership.

`pkg/scheduling` provides a durable FIFO admission queue. A trusted host selects
the pool, its capacity and its store; callers cannot set them in scenario TOML.
Each execution consumes one slot from admission through cleanup. v1 supports
capacities from 1 to 64 and at most 10,000 outstanding entries per pool. A full
queue rejects the new request explicitly; it does not replace existing entries.

Use a stable Kind runner-pool identity to limit simultaneous clusters. Use a
stable AWS stack/workload identity to limit pressure on an existing stack. Never
include an execution ID in the pool identity. Separate pools have independent
limits; the host must place competing workloads in the same pool. v1 does not
provide weighted slots, multiple-pool reservations or a global worker service.

## Store and ordering

Use `NewMongoStore` with a dedicated collection outside disposable environments.
It uses primary reads, majority read/write concern and revision-based
compare-and-swap. All participating workers must share the store, pool identity
and capacity. A configuration mismatch or corrupt state fails closed. The
process-local `MemoryStore` exists only for tests.

Enqueue assigns a durable monotonic sequence. The oldest waiting entries receive
available slots, regardless of their polling speed. With capacity greater than
one, eligible executions may start concurrently. FIFO begins at successful
enqueue, not at workflow dispatch: runner assignment and validation can change
arrival order. The scheduler does not control GitHub's runner allocation.

Avoid GitHub concurrency groups that replace pending runs ahead of this queue.
Do not assume that waiting for admission saves a runner: this in-process design
still occupies a worker while it waits. It bounds test-environment capacity, not
all CPU use or every workflow on that runner. Integrate every relevant execution
path before claiming an aggregate limit.

The queue retains request/worker identities and status, not an executable request
payload. GitHub retains the workflow and request artifacts. The scheduler is not
a workflow dispatcher and does not automatically resume a lost worker.

## Receiver integration

Configure `receiver.Host.Scheduling` with the scheduler, a stable worker identity
and a bounded capacity-safety verifier. Spex admits and plans the request before
enqueue. It acquires capacity before resource claims and environment mutation;
capacity remains occupied while waiting for resource claims. This conservative
ordering can reduce throughput but avoids holding application claims while
waiting for capacity.

After execution and cleanup, the host verifies stopped work and capacity cleanup.
Spex releases the slot only on verified success. It records `scheduling.json` and
updates the result before persisting the receiver receipt. Failed assertions and
cancellation remain the primary outcome when capacity release also fails.

The scheduling API remains optional for existing Spex hosts. A runtime that
requires capacity admission must enforce it in its trusted entrypoint. Scenario
inputs cannot configure or bypass that requirement.

For a host that spans multiple workflow steps, persist the admission report and
use `Resume` to reattach to that same running ticket. Bind it to the current
execution identity before calling the API. Resume checks the pool, capacity,
request, worker, owner and sequence against stored state; it never admits a new
execution and refuses released or recovery-required entries. Keep the ticket
through every selected group and verify owned cleanup before `Finish`.

A host may permit unscheduled execution when no scheduling backend is configured.
It should report that mode explicitly: isolation and resource-safety requirements
still apply, but no scheduler capacity limit exists. Once an administrator
configures scheduling, invalid configuration or backend failure must stop
execution, never trigger an automatic unscheduled fallback.

## Cancellation and recovery

Cancellation withdraws a confirmed waiting entry. A running or uncertain slot
does not expire when a timeout or heartbeat interval elapses. Failed verification
marks it `recovery_required`; an unknown write acknowledgement requires inspection
because the store may have committed it.

Use `Inspect` to read a detached snapshot. To recover an entry, the host must
verify that its worker stopped and capacity is safe, then call `Recover` with an
evidence reference and retain the returned entry with that evidence. For Kind,
inspect the ownership ledger and remove only verified execution-owned resources.
For AWS, separately verify application resource safety before releasing resource
claims. Releasing capacity does not release those claims.

Never clear an entire queue, rename a pool or change capacity to bypass retained
owners. v1 pins capacity in stored state; resizing requires a reviewed migration
after all workers stop and all entries are reconciled. Configure database access
and backup/recovery policy independently of test execution. Never provide store
credentials to caller definitions or test pods.

This API requires a release newer than `v0.1.0-rc.39`.
