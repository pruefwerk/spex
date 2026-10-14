# Security review notes

Complete this checklist before enabling production use in a scenario repository.

## Secrets

- Scenario parameters are non-secret only.
- Credentials must flow through `spec.secrets`.
- URLs must not include userinfo.
- Reports and generated manifests must contain only Secret names, key names, env var names, and SSM paths.
- `spex doctor --scan-artifacts` must run with realistic `SPEX_*` secret environment variables present and must fail the job if kubeconfig files are still present.
- Live CI jobs must remove generated kubeconfig files before uploading artifacts.

## External references

- Pin Git refs to immutable tags or commit SHAs.
- Use `--require-pinned-git-refs` in CI.
- Keep Git cache directories out of retained artifacts unless explicitly needed.

## Images and charts

- Pin production probe images by digest.
- Pin Helm chart versions.
- Keep Helm values files in reviewed repositories.
- Do not install fake services unless the integration profile explicitly sets `allowFakes: true`.

## Generated commands

Review integration profile commands for:

- shell expansion of secrets
- literal credentials
- URL userinfo
- mutable image tags
- placeholder registries
- unexpected local paths

Review the profile even when the loader accepts it; the loader blocks only common unsafe patterns.

## Artifacts

Retain reports and evidence for failed runs, and scan them for secret values before upload. Avoid uploading local Git caches, kubeconfig files, or unrelated workspace directories. Live KUTTL logs may include host-local paths and Docker diagnostics. Keep them as CI evidence outside source-controlled fixtures.
