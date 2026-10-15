# Configured runtime host

Spex supplies the executable host for `migration-testbench/v1`. A runtime
repository supplies a trusted TOML definition, existing suite/binding/profile
files, fixtures and service setup scripts for that host.

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

Keep this definition in the trusted runtime checkout, outside the caller's
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

### Additional trusted suites

Register other Kind suites in the same host definition:

```toml
[[suites]]
suite = "suite-kind-cache.yaml"
include_images = ["probe", "http-probe:test"]
image_services = ["cache"]
health_resources = ["cache"]
```

`include_images` selects entries from the definition's `images` inventory by
alias. The selector `probe` selects the entry with `probe_alias = true`.
`extra_images` accepts the same typed image-build entries for suite-specific
fixtures. Extra build contexts also become fingerprint inputs.

The registered suite's YAML supplies `bindingRef` and `integrationProfileRef`;
the profile supplies its Kind topology. Spex confines these references to the
trusted runtime checkout. No second binding, profile or topology model is needed.
Image selections, image services and health-resource arrays replace the default
suite's inventories; omitted arrays are empty. Admission, scheduling, credentials
and other host policy remain inherited. The default suite stays unchanged.

Select a registered suite with `execution ... --suite <suite.yaml>`. Spex keeps
its original identity in `SPEX_RUNTIME_SUITE` while `SPEX_SUITE` points to the
execution-specific projection. `prepare-start` writes both into `GITHUB_ENV`,
and profile subprocesses inherit the same selection. These values never authorize
an unregistered suite or adoption of another suite's resources. For split CI,
`group-run --ci-group suite` executes the whole suite without a tag filter.

An execution-gate failure prints a fixed, secret-safe diagnostic and attempts to
retain the first failure in `.spex/runtime-diagnostics/execution-gate.json`, even
when no scope exists yet. Its schema is `spex.execution-diagnostic/v1`; reason
codes are `SUITE_NOT_CONFIGURED` and `EXECUTION_GATE_FAILED`. Wrapped causes and
configuration values never enter this diagnostic. Hosts should upload this
directory on failure alongside normal scenario evidence.

## Execution and recovery

Split CI commands `prepare-start`, `check`, `group-run` and `cleanup` compose
the same owned lifecycle as one-shot `execution run`. Admission checkpoints and
capacity tickets persist between processes. Cleanup releases capacity only after
owned resources disappear. Cancellation propagates from the executable.

KUTTL replaces `KUBECONFIG` with its workspace-local copy when invoking profile
commands. The host's `execution profile` preflight therefore derives the original
kubeconfig path from the execution scope instead of treating that environment
variable as ownership identity. It still verifies the projected configuration,
expected context, policy fingerprint, and exact live node IDs. An explicitly
supplied `--kubeconfig` must match the execution-owned path.

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

Source admission can use an organization boundary instead of individual caller
lists:

```json
{
  "schema": "spex.receiver-source-policy/v1",
  "sources": [
    {"organization": "example-org", "visibilities": ["internal", "private"]}
  ]
}
```

Spex verifies organization ownership and visibility through GitHub before fetching
source artifacts. Organization entries must declare `internal`, `private`, or
both; they cannot admit public repositories. GitHub credentials and permissions
control who can dispatch. Action/reusable-workflow access settings do not replace
this check of caller-supplied source identities.

Each entry has exactly one selector: `organization` or an exact `repository`.
Optional `workflow`, `branch` and `actors` fields narrow that entry. Omit them to
avoid maintaining per-workflow, branch or developer lists. A present `actors`
array must not be empty. Entries are alternatives: one matching entry admits a
source. Existing exact-repository policies remain supported; an empty `sources`
array denies all. Unknown fields and malformed selectors fail closed.

The host still verifies the active first source-run attempt, excludes forks and
pull-request runs, correlates artifact/run/commit identities and checks artifact
age. Request digest verification and durable replay claims remain separate gates.
Earlier releases without organization-selector support reject these new fields;
upgrade the host executable before deploying an organization policy.

### Admission failure diagnostics

Both `source-admit` and `source-admit-reusable` exit with code 1 when admission
fails. They print the existing failure message followed by one JSON diagnostic:

```json
{"schema":"spex.admission-diagnostic/v1","stage":"github_api","reason":"ARTIFACT_METADATA_UNAVAILABLE","classification":"external_operation_failure"}
```

| Stage | Reason codes | Classification |
| --- | --- | --- |
| `identity_validation` | `IDENTITY_MISMATCH`, `RUN_ATTEMPT_UNSUPPORTED`, `ARTIFACT_IDENTITY_MISMATCH`, `ARTIFACT_AGE_INVALID` | `admission_rejection` |
| `admission_policy` | `POLICY_REJECTED` | `intentional_refusal` |
| `admission_policy` | `POLICY_UNAVAILABLE` | `configuration_failure` |
| `github_api` | `REPOSITORY_METADATA_UNAVAILABLE`, `RUN_METADATA_UNAVAILABLE`, `WORKFLOW_METADATA_UNAVAILABLE`, `ARTIFACT_METADATA_UNAVAILABLE` | `external_operation_failure` |
| `artifact_retrieval` | `ARTIFACT_DOWNLOAD_FAILED` | `retrieval_failure` |
| `source_retrieval` | `SOURCE_DOWNLOAD_FAILED` | `retrieval_failure` |
| `local_preparation` | `SOURCE_EXTRACTION_FAILED`, `PREPARATION_FAILED`, `RUNTIME_IDENTITY_INVALID` | `local_execution_failure` |
| `unknown` | `ADMISSION_FAILED` | `unknown_failure` |

Classification occurs where each failure originates. Output contains only the
listed constants; wrapped errors, HTTP bodies, credentials, signed URLs and
caller-provided text never enter the diagnostic. Unknown errors use the explicit
fallback. Diagnostics do not change admission decisions, retry behavior or exit
codes. An external-operation failure does not imply that retrying is authorized
or will succeed. Retryability is omitted because these checks cannot reliably
determine it.

For `RUN_ATTEMPT_UNSUPPORTED`, start a new workflow run and prepare a new request.
Rerunning jobs increments the run attempt and remains unsupported. For other
codes, investigate the named stage before repeating the run. A diagnostic identifies
the failing operation; it does not by itself establish its underlying cause.

### Caller-owned acceptance definitions

The receiver can resolve caller tests against runtime-owned catalogs, bindings and
setup. Enable this in the trusted host definition, not in the caller request:

```toml
caller_tests = true
caller_configuration = ["configuration.binding", "configuration.catalogs", "probe.image"]

[repository_defaults]
"example-org/service" = "runtime/service-defaults.toml"
```

`repository_defaults` selects a runtime-config TOML fragment by the authenticated
source repository. The fragment and its configuration references stay inside the
trusted runtime checkout. Repository defaults are optional; without a matching
entry, the existing suite remains the inherited base.

A caller can submit a scenario TOML through the existing Action definition inputs:

```toml
schema = "spex.scenario/v1"
runtime = "migration-testbench/v1"

[[tests]]
file = "acceptance/migration.feature"

[runtime_config.configuration]
binding = "acceptance/binding.yaml"
catalogs = ["acceptance/steps.yaml"]
```

Omit `configuration` to inherit the host binding and catalog. Omit `tests` to
discover the existing host suite. Caller file references resolve inside the
verified source checkout; inherited setup and credential hooks still use the
runtime checkout. The Action neither merges these layers nor selects defaults.

Resolution applies the suite, optional host repository defaults, and caller
scenario configuration in that order. YAML binding overlays use strict typed
decoding: omitted fields inherit, explicit false/zero values replace, arrays
replace, and map entries replace by key while unmentioned keys inherit. Catalog
overlays inherit unmatched definitions and replace equal flow names or exact
step kind/expression pairs. Later catalog files win; different expressions that
match the same sentence still trigger the existing ambiguity error. Catalog
reference arrays replace within TOML authoring merges; the selected catalogs
then layer over the inherited catalog definitions.

Host guardrails reject unapproved runtime-config fields before planning. The
existing default admits only group selection and no caller tests. Hosts can admit
the listed binding/catalog fields, probe image fields, selected execution counts,
fail-fast/resource-usage settings and tag exclusion. Suite replacement, credential
hooks, namespace/context overrides and retained resources remain receiver-owned.
Resolved bindings must retain the execution-owned namespace and Kind context;
Spex rejects an incompatible binding instead of silently rewriting it.

Catalogs and YAML operations describe executable test behavior. Admit them only
from source repositories trusted to author tests for this runtime's permissions.
Field guardrails and isolated Kind resources do not sandbox arbitrary catalog
behavior or establish safe AWS execution. Review new definitions accordingly.

Do not put literal secrets in the host definition. Use credential references
and the existing private hook environment. The host never prints its raw policy
or persists expanded credentials. Runtime-produced evidence remains subject to
the runtime's artifact and redaction policy.
