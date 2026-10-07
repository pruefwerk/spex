# Remote submission preview

The current spex-action no longer invokes the CLI transport described below. It
sends an opaque `spex.transport/v1` authoring envelope and immutable source commit
directly to a receiver, which owns its Spex version. The commands below remain
available for standalone/legacy clients. The new Action contract uses the public
[receiver API](receiver.md). Runtime repositories must still implement and qualify
their own GitHub workflow, authenticated source fetching and admission policy.

This adds a client transport around the existing in-process runtime. The receiver
still runs Spex locally; no plugin ABI or subprocess runtime protocol is introduced.
The GitHub Action packages/uploads/submits from the caller repository instead of
preparing the acceptance environment in the caller job.

## Unified authoring

`scenario build` and `scenario package` accept either `--definition-inline-file`
(a transport file holding inline content) or repeatable `--definition-file`
(workspace-relative paths or glob patterns). Spex recognizes scenario/request TOML, Spex YAML and
Gherkin. A complete TOML definition must stand alone: no extra tests and no second
TOML definition. Inline YAML/Gherkin supports `---` separators; external test files
contain one document, with `.feature` for Gherkin. Omit both to retain discovery.
Do not mix these flags with legacy source flags, which remain supported separately.

TOML definitions can declare `dependencies = ["fixtures/reading.json"]` at the
top level. Paths use the workspace root, are normalized and checked for escape,
and are packaged without execution or secret expansion. There is no implicit
dependency discovery or new YAML/Gherkin fixture directive. Unknown formats and
ambiguous input combinations fail before packaging or environment preparation.

Definition globs use `*`, `?`, character classes, and a whole path component `**`
for zero or more directory levels. Matches are sorted and deduplicated; every
pattern must match a file. `.git` is skipped; directory symlinks are not traversed;
file symlinks must resolve within the source root. Scans stop after 100,000 entries
or 1,000 matches. Regex and exclusion syntax are not supported. Globs apply to
definition-file arguments, not dependency declarations. In the Action, the source
base comes from its actual current directory relative to the workflow workspace;
the receiver chooses its own checkout locations and performs expansion at the
source commit. CLI --workspace remains available for explicit local execution.

`spex scenario package` accepts the authoring flags from `scenario build` plus
repeatable `--support-file`. Runtime selection is optional: omit it for the receiver
default or pass `contract/vN@release` for a supported immutable release. A bare
contract uses its configured default release. The receiver owns typed validation.
It writes a `spex.scenario-request/v1` TOML authoring document, a deterministic
`scenario-package.zip`, and v2 `request.json`
beside the `--out` path. File references are workspace-relative; only selected files
are included. Limits: 1,000 ZIP entries and 16 MiB uncompressed. The semantic scenario
hash excludes execution IDs; the package digest also covers external source bytes.

`--inline-file` in both `scenario build` and `scenario package` accepts multiple
inline sources separated by standalone column-zero `---` lines. Each document
becomes one canonical `tests[]` entry; the runtime detects YAML or Gherkin for
each source. Indented markers remain content. A leading marker is allowed;
empty documents, including a trailing separator, are rejected with their document
number and starting line. Runtime validation identifies sources by the existing
zero-based `scenario.toml:test[index]` location. Committed TOML inline entries and
external files retain their existing semantics; only authoring input is split.

Upload the ZIP as `spex-request-<request_id>` in the caller's GitHub run, then invoke:

```sh
spex scenario submit --workspace . --request .spex/request/request.json \
  --runtime-workflow your-org/runtime/.github/workflows/acceptance.yaml@runtime-v1 \
  --source-repository your-org/service --source-run-id 123 --artifact-id 456 \
  --wait-timeout 5h
```

Supply `SPEX_REMOTE_TOKEN` through the environment, never argv or scenario text.
The workflow reference requires an explicit branch or tag. Spex parses it; no
adapter needs to split it into dispatch coordinates. Existing CLI callers can
still use `--repository`, `--workflow` and `--ref` together, but cannot combine
those flags with `--runtime-workflow`.
It requires Actions write on the receiver, source artifact read access and commit
read access. The first transport uses GitHub.com only. Ref resolution pins the
expected workflow commit; a run at a different SHA is rejected.

The receiver must implement `spex.submission/v2`: unique `spex:<request_id>` run
names, authenticated source-artifact retrieval, digest/source admission, bounded
execution and an identity-bound `spex.remote-result/v2` receipt. The full contract
is in spex-action's `docs/remote-protocol.md`. An existing arbitrary workflow does
not satisfy this contract merely because it accepts workflow_dispatch.

`scenario.ResolveRequest` selects from a trusted release catalog and invokes only
the selected runtime's typed configuration merger. It produces an executable
scenario with an exact `runtime_release`. Registered pinned implementations must
expose `Release() string`; missing or mismatched releases fail before execution.
The receiver retains this final canonical artifact and reports its resolved hash
and release separately from the submission hash. Local run/validate commands do
not accept unresolved requests. Previously prepared v1 packages remain supported.

The client dispatches once and polls every ten seconds. An exclusive dispatch
marker prevents blind local replay. It refuses duplicate runs, reruns, missing
receipts, identity mismatches and inconsistent success. Queue/execution wait is
bounded; interrupted waits attempt cancellation with an independent ten-second
budget. Cancellation cannot be guaranteed after hard caller loss, delayed run
discovery, lost authorization or token expiry. Receivers must enforce timeouts.

The retained local result projects structured outcomes and discards arbitrary
receiver messages, test names and artifact paths. Detailed evidence remains in
the remote run. Local reproduction still needs the runtime's trusted configuration
and dependencies. Literal secrets in explicitly supplied input are prohibited;
the packager cannot recognize arbitrary secret values.

Remote commands are not in rc.38. Local CLI execution and existing suite
entrypoints remain available; the Action only submits remote runs.
The client is unit-tested against a mock API; the
testbench receiver, live dispatch, token renewal and hosted end-to-end qualification
remain pending. No production CI defaults change as part of this preview.
