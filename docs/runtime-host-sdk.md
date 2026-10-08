# Runtime host SDK

Use `pkg/runtimehost` when a trusted runtime spans separate processes or CI
steps. It composes the existing scheduler; it does not implement another test
engine, choose an environment or generate workflows.

`Session` persists an execution identity and admission mode. Call `Gate` with
`Acquire` once, `Check` before each subsequent step and `Finish` during recovery
or final cleanup. A scope cannot be acquired twice. Checks reject changed
identity, changed scheduling mode and completed execution.

The host supplies the private state directory, schema identifiers, scheduling
callback and ownership-checked cleanup callback. Existing checkpoint encodings
remain usable. State files are private, bounded and strictly decoded.
Keep this directory outside caller-controlled source.

`Capacity` handles a durable scheduler ticket across those steps. The host
supplies its scheduler factory, report path, resource cleanup callback and
cleanup/release deadlines. It admits once, resumes the same ticket for checks,
and releases only after cleanup confirms safety. Reports use atomic replacement.
Failed cleanup retains recovery-required capacity. Failed admission does not
silently disable scheduling.

Configure an unscheduled `Session` when the host intentionally has no scheduler.
Ownership and completion checks still apply, but no aggregate capacity limit
exists. During cleanup, the recorded mode takes precedence over current credential
availability; missing credentials cannot authorize release of a scheduled ticket.

## Configured executable

Projects that use the built-in Kind host should configure
[`spex runtime`](configured-runtime-host.md) instead of compiling these mechanisms
into a project-specific executable. The SDK remains available for host mechanics
that need direct composition.

`engine.New` creates a host with an immutable definition and version snapshot;
`Host.Run` executes its configured operations. `definition.NewResolver` provides
instance-owned scope and policy resolution. Prefer these instance APIs over the
compatibility `Bind`/`Current` functions. Host operations restore their temporary
environment settings and reject concurrent environment-dependent calls within
one process. Separate processes can execute concurrently under the same ownership
and scheduler rules.

## Runtime boundary

The configured host owns source admission, configuration/profile projection and
scope validation. Projects supply resource selection, store configuration and
cleanup policy as typed trusted data. The SDK owns
checkpoint persistence, cross-step state checks, ticket resumption and release
ordering. Cleanup callbacks must honor their contexts. Use the existing
`scenarioruntime.Lifecycle` for in-process setup/test/cleanup and primary outcome
preservation; these APIs are complementary, not separate execution engines.
