# Scenario authoring preview

This checkpoint supports `spex scenario build`, `validate`, `explain` and `run`.
The local `pruefwerk/spex-action` implementation now supports these commands;
hosted Action and live runtime qualification remain unfinished. Continue to
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

Existing profiles can still contain working-directory-relative paths. Run from
the runtime repository root when using those profiles; `--workspace` does not
change the process working directory or reinterpret legacy profile paths.

## Selection and credential renewal

The migration runtime accepts `selection.include_tags`, `include_any_tags` and
`exclude_tags` arrays inside `runtime_config`. They use the existing suite tag
filters: all required tags, at least one optional tag, and no excluded tags.
An absent array inherits; a supplied array replaces it, including `[]` to clear
the filter. Selection feeds the existing discovery machinery. Other value
overrides still apply after inherited configuration resolves.

`execution.before_scenario_hook` selects an existing trusted executable relative
to the workspace. The runtime rejects escaping paths and non-executable files.
An explicit empty string clears the hook. `before_scenario_hook_timeout` accepts
a positive duration up to ten minutes; the existing default is two minutes.
Validation and explanation never invoke the hook. Execution invokes it before
each selected test, including repetitions, using the existing JSON environment
contract. Credentials remain in memory and do not enter canonical artifacts.
Only select reviewed repository code: a hook is executable code, not untrusted
scenario text. This exposes an existing runtime facility, not a generic lifecycle
language or a GitHub-specific credential mechanism.

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

`spex reports scenario --out DIR` checks one invocation's canonical result and
prints identity, outcome and test counts. Use a fresh artifact base for each CI
invocation, or point directly at one execution directory. The reader rejects
missing, mismatched or multiple execution records and incomplete success claims.
It prints no free-form backend messages. A collector failure should fail an
otherwise successful CI step, but must not replace an existing execution failure
or timeout. `reports summarize` continues to support legacy suite workspaces.

## Current qualification boundary

Local tests exercise YAML and Gherkin authoring, committed-document overrides,
source equivalence, unknown-field rejection, shell-looking input, symlink
confinement and non-overwriting output. Execution equivalence tests use a local
test runner, not live Kubernetes services. Fresh live acceptance remains blocked
by the infrastructure listed in the architecture qualification report.

Hosted Action qualification and testbench CI adoption remain outstanding. The preview
does not expand secret references into canonical TOML. Never place literal
credentials in inline test text or scenario metadata. Live acceptance, credential
renewal across long runs and service-repository adoption still require qualification.
