# Reports and diagnostics

Summarize generated workspaces, including nested group directories:

```sh
spex reports summarize --out generated --format text
spex reports summarize --out generated --format json
```

Spex lists passes, failures, unreadable reports, and workspaces without a final
report. It skips symlinks and rejects oversized report files. When a workspace
contains scenario identity and a run ID, Spex rejects a report from another run.
`workspaceReportsComplete` means every discovered workspace has a readable final
report. Scenarios whose workspaces were never generated cannot appear in the
summary, so it cannot prove that the intended suite finished. Keep the execution
exit code as the result; the inspection command does not replace it.

For CI groups, provide `group-plan.json` in a directory. Map each group name to
`scenarios` (count) and `selected` (boolean). Write `<group>-result.json` with
`exitCode` after execution. Clear previous result files when creating a new plan.

```sh
spex reports groups --root artifacts --format markdown --title "Acceptance groups"
```

Spex reports exit code 0 as passed, 124 as timed out, and other codes as failed.
It marks selected groups without an execution result as not run and returns a
nonzero exit code unless every selected group passed. Group summaries describe
execution results and cannot independently prove scenario coverage. The CI adapter
chooses group names, execution order, and where to publish the Markdown.

Collect Kubernetes diagnostics with an explicit scope:

```sh
spex diagnostics collect --kubeconfig kubeconfig --namespace acceptance \
  --selector spex/owned=true --tail 300
spex diagnostics previous-logs --kubeconfig kubeconfig --namespace acceptance \
  --selector app.kubernetes.io/instance=example
```

Collection reads resources, namespace events, pod descriptions, and current logs.
It requests previous logs only for containers whose restart count exceeds zero,
including init containers. It never creates or deletes resources. Each command
has a deadline and a 1 MiB output limit; collection has a total deadline (default
two minutes). If collection fails, diagnostics are incomplete but the test result
stays unchanged. Callers retain the original test exit code.

Logs and pod descriptions can contain sensitive application data. Restrict access
to diagnostic artifacts and use the same retention policy as application logs.

## Scenario-runtime failures

Portable scenario runs do not publish raw KUTTL output, generated manifests, or
resolved bindings. Those files can contain credentials from setup commands and
backend responses. Before deleting the private workspace, Spex retains a typed
runner diagnostic in `artifacts/test-evidence.json` and summarizes it in
`result.json`.

The diagnostic identifies the last observed phase, a fixed reason hint, the
runner exit code when available, and the number of the last observed profile
command (starting at 1). Match that number to the integration profile's
`kind.commands` list. It identifies the last command observed starting; it does
not prove that command caused the failure. Reason hints come from recognized
runner messages and may be incomplete. No command text, response text, or
filesystem paths enter this diagnostic.

A later `ci health` failure describes the environment after the original
failure. Its output distinguishes an API-readiness failure, a failed workload
query, missing required workloads, and unready replicas. Keep the original run
result when interpreting these secondary diagnostics.
