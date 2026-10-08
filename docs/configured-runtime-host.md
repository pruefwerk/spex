# Configured runtime host

Spex supplies the executable host for `migration-testbench/v1`. A runtime
repository supplies a trusted TOML definition, existing suite/binding/profile
files, fixtures and service setup scripts. It does not implement another host.

Run from the runtime checkout:

```sh
spex runtime --config spex-runtime.toml validate
spex runtime --config spex-runtime.toml execution explain --groups verification
spex runtime --config spex-runtime.toml execution run --groups verification
```

The host resolves inherited configuration through the existing runtime resolver,
then applies the scenario overlay. Suite discovery and committed scenario TOML
enter the same execution path. `validate` checks the host definition only;
`execution explain` also resolves and plans the selected tests without creating
an external environment.

## Host definition

A minimal definition names existing environment configuration:

```toml
schema = "spex.runtime-host/v1"
runtime = "migration-testbench/v1"
suite = "suite-kind-example.yaml"
binding = "bindings/local.yaml"
profile = "integration/local.yaml"
topology = "integration/node.yaml"
namespace = "acceptance"
```

The profile must contain the host-owned image preparation entrypoint. Projects
can keep existing `.spex/bin/runtime-host` callers through a small shell adapter
that invokes the command above with an absolute definition path.

`pkg/runtimehost/definition.Definition` defines the strictly decoded fields:

| Configuration | Purpose |
| --- | --- |
| Suite, binding, profile and topology references | Select trusted inherited environment inputs |
| Scope/schema prefixes and environment key | Preserve host identity and existing recovery formats |
| Groups, baseline tag and summary title | Configure selection and CI accounting |
| Source/scheduling policy references and pool | Select trusted admission and capacity policy |
| Image builds, pinned services, mirror inventory and fingerprint inputs | Configure owned image preparation |
| Setup tag defaults, profiles and overrides | Produce data-only environment values for existing setup scripts |
| Resource types | Normalize resource coordination identities |
| Readiness and audit fields | Configure optional read-only helpers |
| Credential role and before-scenario hook | Configure trusted credential renewal |

Unknown fields fail validation. Setup profiles and overrides may change only
declared default keys. Arrays remain explicit policy data; the host does not
recursively merge arbitrary caller maps or evaluate configuration as code.

This definition belongs to the trusted runtime checkout, not the caller's
scenario. Callers cannot disable ownership checks, replace the suite topology or
bypass a configured scheduler. A prepared execution records a definition
fingerprint; subsequent execution checks refuse policy drift. Exact-ledger
cleanup remains available for recovery.

Each host snapshots its definition and version when it is constructed. Loading
another host cannot change that snapshot or rename its execution scopes. The
inherited suite resolver still reads process environment, so Spex serializes
environment-dependent host operations within one process and restores the
host-owned environment values afterward, including on failure and cancellation.
Use separate processes for concurrent executions; ownership checks and configured
scheduling still apply across those processes.

## Execution and recovery

Split CI commands `prepare-start`, `check`, `group-run` and `cleanup` compose
the same owned lifecycle as one-shot `execution run`. Admission checkpoints and
capacity tickets persist between processes. Cleanup releases capacity only after
owned resources disappear. Cancellation propagates from the executable.

No scheduler URI means isolated execution without an aggregate concurrency
limit. A configured but unavailable store blocks admission; it does not fall back
to unscheduled execution. AWS does not become supported merely by changing the
definition: shared-cluster admission and resource/effect policy need qualification.

The scheduling JSON policy can declare `connection_string` as a MongoDB URI or
an exact environment reference such as `${SPEX_SCHEDULING_MONGODB_URI}`.
Use a reference for credential-bearing connections. An unset referenced variable
disables scheduling; an invalid declared policy or nonempty invalid connection
blocks admission. Existing policies without this field retain the previous
`SPEX_SCHEDULING_MONGODB_URI` convention. Connection strings never enter capacity
receipts or error messages.

Spex owns the policy schemas `spex.receiver-source-policy/v1` and
`spex.scheduling-policy/v1`. Runtime repositories own the policy instances and
their values. The host's `schema_prefix` names private execution ledgers, not
new policy formats. Earlier prefix-based policy identifiers remain accepted
for compatibility; use the Spex identifiers for new configuration.

Do not put literal secrets in the host definition. Use credential references
and the existing private hook environment. The host never prints its raw policy
or persists expanded credentials. Runtime-produced evidence remains subject to
the runtime's artifact and redaction policy.
