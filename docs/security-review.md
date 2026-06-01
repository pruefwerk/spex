# Security Review Notes

Run this checklist before enabling production use in a scenario repository.

## Secrets

- Scenario parameters are non-secret only.
- Credentials must flow through `spec.secrets`.
- URLs must not include userinfo.
- Reports and generated manifests must contain only Secret names, key names, env var names, and SSM paths.
- `spex doctor --scan-artifacts` must run with realistic `SPEX_*` secret environment variables present and must fail the job if kubeconfig files are still present.
- Live CI jobs must remove generated kubeconfig files before uploading artifacts.

## External References

- Pin Git refs to immutable tags or commit SHAs.
- Use `--require-pinned-git-refs` in CI.
- Keep Git cache directories out of retained artifacts unless explicitly needed.

## Images and Charts

- Pin production probe images by digest.
- Pin Helm chart versions.
- Keep Helm values files in reviewed repositories.
- Do not install fake services unless the integration profile explicitly sets `allowFakes: true`.

## Generated Commands

Review integration profile commands for:

- shell expansion of secrets
- literal credentials
- URL userinfo
- mutable image tags
- placeholder registries
- unexpected local paths

The loader blocks common unsafe patterns, but this is not a substitute for profile review.

## Artifacts

Retain reports and evidence for failed runs. Scan retained artifacts for secret values before upload. Avoid uploading local Git caches, kubeconfig files, or unrelated workspace directories. Live KUTTL logs may include host-local paths and Docker diagnostics; treat them as CI evidence, not source-controlled fixtures.
