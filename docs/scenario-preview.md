# Scenario authoring preview

This checkpoint supports `spex scenario build`, `validate`, `explain` and `run`.
The GitHub Action and live runtime qualification remain unfinished. Continue to
use the existing suite commands for production acceptance runs until qualification
finishes.

## Configuration and paths

Select an existing suite with `runtime_config.suite`, the inherited `SPEX_SUITE`
environment variable, or a repository-root `suite.yaml`, in that order. The
runtime uses the existing suite, binding, catalog and profile loaders. It then
applies typed scenario overrides. A scenario does not supply service defaults.

`--workspace` selects the repository root; it defaults to the current directory.
External test paths and relative authoring-input paths resolve against that root,
not against the TOML file's directory. Explicit absolute authoring-input paths
are allowed. Test references must stay inside the workspace, including through
symlinks. Existing test-file include semantics remain unchanged. Inline sources
use the workspace as their source directory.

Spex accepts its existing YAML test definitions and Gherkin features. A `.spex`
file contains YAML. Inline text beginning with a Gherkin `Feature:` declaration
(after comments or tags) uses the existing Gherkin parser. Inline sources do not
introduce another language or require a temporary file during validation.

## Commands

`spex scenario build` accepts `--runtime`, optional `--name`, `--description`,
`--timeout`, and either `--inline-file` or repeatable `--spex-file` arguments.
`--runtime-config-file` reads a TOML fragment without shell interpolation.

Alternatively, supply `--scenario` to start from committed TOML. Explicit
metadata and runtime configuration override that document. Do not combine it
with test-source arguments. If you also provide `--runtime`, it must match.

The builder resolves and validates the scenario before writing canonical TOML.
`--out` defaults to `.spex/scenario.toml`, remains confined to the workspace,
and must name a new file. The builder does not overwrite an existing file.

`spex scenario validate [--workspace DIR] FILE` validates the document, runtime,
configuration, test sources and plan without deploying anything.

`spex scenario explain [--workspace DIR] FILE` emits JSON containing the full
scenario ID, schema, runtime, name and plan summary. It does not print the resolved
configuration. Inline sources use `scenario.toml:test[n]` as their logical source.

`spex scenario run [--workspace DIR] [--artifact-directory DIR] FILE` executes
the prepared scenario through the shared suite executor. The artifact directory
defaults to `.spex/runs` and must remain inside the workspace. Each execution
gets its own directory beneath the full scenario hash; reruns never overwrite
earlier evidence. The CLI emits a JSON summary with the scenario ID, runtime,
outcome and paths to the canonical scenario, result and artifact directory.

Each completed run retains `scenario.toml`, `plan.json`, `runtime.json`,
`result.json` and `artifacts/test-evidence.json`. The evidence file records test
and step outcomes in the same order as the result's test list. It excludes raw
backend responses, free-form error messages, generated manifests, bindings and
kubeconfig. This trades detailed troubleshooting output for a strict export
boundary; it is not a replacement for complete service-side diagnostics.

The runtime uses a private temporary workspace for existing tools and removes
it after execution. Those tools can write sensitive transient files there. Do
not archive temporary directories; a forced kill or host crash may prevent their
removal. No resolved configuration or transient tool output is copied into the
run artifact directory.

Cancellation and the scenario execution timeout stop runtime commands, then
allow bounded cleanup. Exit codes are 0 for success, 2 for validation errors,
3 for mapped test failures, 4 for infrastructure/artifact failures, and 130 for
cancellation. A reporting or cleanup failure does not replace a primary test
failure. If storage fails, the result file itself may be unavailable; the CLI
still returns failure.

## Current qualification boundary

Local tests exercise YAML and Gherkin authoring, committed-document overrides,
source equivalence, unknown-field rejection, shell-looking input, symlink
confinement and non-overwriting output. Execution equivalence tests use a local
test runner, not live Kubernetes services. Fresh live acceptance remains blocked
by the infrastructure listed in the architecture qualification report.

The GitHub Action and testbench CI adoption remain under development. The preview
does not expand secret references into canonical TOML. Never place literal
credentials in inline test text or scenario metadata. Live acceptance, credential
renewal across long runs and service-repository adoption still require qualification.
