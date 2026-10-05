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
report. It does not prove that the intended suite finished: scenarios whose
workspaces were never generated cannot appear in this summary. The inspection
command does not replace the execution exit code.

For CI groups, provide `group-plan.json` in a directory. Map each group name to
`scenarios` (count) and `selected` (boolean). Write `<group>-result.json` with
`exitCode` after execution. Clear previous result files when creating a new plan.

```sh
spex reports groups --root artifacts --format markdown --title "Acceptance groups"
```

Spex reports exit code 0 as passed, 124 as timed out, and other codes as failed.
It marks selected groups without an execution result as not run and returns a
nonzero exit code unless every selected group passed. Group summaries describe
execution results, not independent proof of scenario coverage. The CI adapter
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
two minutes). Failures produce an incomplete diagnostic result, not a changed
test result. Callers retain the original test exit code.

Logs and pod descriptions can contain sensitive application data. Restrict access
to diagnostic artifacts and use the same retention policy as application logs.
