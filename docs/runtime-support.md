# Native host helpers

Spex provides portable Go helpers in `pkg/runtimehelpers`. Hosts use the SDK or
the same command handler through the pinned executable. No Python installation,
embedded source library or dynamic loader is required.

`spex runtime-support console LOG -- COMMAND ARG...` captures the full child
stream in a private artifact, streams scenario progress and prints bounded
failure excerpts. It preserves child exit codes and terminates its child process
group on cancellation. It passes argv directly, without shell interpolation.
It does not redact application output; hosts must control logging and protect
the full artifact.

`spex runtime-support helm --namespace NS --receipt-prefix PREFIX --manager HOST
--timeout 8m -- KUBECONFIG RELEASE CHART OPTIONS...` installs an absent release
or verifies a recorded release before reuse. The host owns every policy value,
chart pin and option. Spex supplies no service topology or defaults.

Helm receipts hash requested options/files and observed metadata/values. The
helper checks one explicit revision and rejects untracked, failed, pending or
drifted releases instead of reinstalling them. Its receipt hash preserves the
previous encoding, including Unicode arguments. Diagnostics and receipts do not
contain expanded Helm values or credentials.

This check does not serialize concurrent Helm writers. Use it in an owned
environment or hold an enforced resource claim. A receipt describes the checked
inputs; it does not prove application correctness or freeze live state.

`RenewAWSCredentials` supplies the portable OIDC/STS mechanism. Hosts pass a typed
role, region, session name and duration policy; Spex does not choose them.
The helper clears stale credentials, validates expiry, keeps token files private
outside the workspace and removes them after use. Returned credentials are
private process input, never normal console output or an evidence artifact.

`receiver.GitHubSource` provides bounded metadata/archive downloads and strips
credentials from approved archive redirects. `receiver.ExtractSource` accepts
bounded regular files/directories as read-only data, without executable bits.
Hosts must authenticate source identities and apply their allowlists first;
these mechanisms do not grant admission or authorize caller code execution.
