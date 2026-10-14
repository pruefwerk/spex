# Security policy

## Supported versions

The maintainer provides security fixes for the latest published release and the current `main` branch.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability.

Email security issues to the project maintainer named in `LICENSE`. You can also use GitHub private vulnerability reporting once the public repository enables it.

Include:

- affected version or commit
- reproduction steps
- expected and observed impact
- whether generated artifacts, reports, kubeconfigs, or logs contain sensitive data

You should receive an initial response within 7 calendar days.

## Handling sensitive artifacts

Generated workspaces, live-run reports, KUTTL artifacts, and kubeconfigs may contain environment-specific metadata. Before uploading or retaining artifacts, run:

```sh
spex doctor --scan-artifacts reports --scan-artifacts generated --format json
```

Provide representative `SPEX_*PASSWORD`, `SPEX_*TOKEN`, and `SPEX_*SECRET` environment variables when running the scan. It fails on matching secret values and kubeconfig files.

## Dependency and release checks

Release candidates should pass:

```sh
make install-vulncheck
make security-check
make production-candidate-check
```

Published release archives should include checksums, release provenance, dependency inventory, Go module inventory, build metadata, and third-party license inventory.
