# Security Policy

## Supported Versions

Security fixes are provided for the latest published release and the current `main` branch.

## Reporting a Vulnerability

Do not open a public issue for a suspected vulnerability.

Report security issues by email to the project maintainer named in `LICENSE`, or through GitHub private vulnerability reporting once it is enabled for the public repository.

Include:

- affected version or commit
- reproduction steps
- expected and observed impact
- whether generated artifacts, reports, kubeconfigs, or logs contain sensitive data

You should receive an initial response within 7 calendar days.

## Handling Sensitive Artifacts

Generated workspaces, live-run reports, KUTTL artifacts, and kubeconfigs may contain environment-specific metadata. Before uploading or retaining artifacts, run:

```sh
spex doctor --scan-artifacts reports --scan-artifacts generated --format json
```

Run the scan with representative `SPEX_*PASSWORD`, `SPEX_*TOKEN`, and `SPEX_*SECRET` environment variables present. The scan fails on matching secret values and kubeconfig files.

## Dependency and Release Checks

Release candidates should pass:

```sh
make install-vulncheck
make security-check
make production-candidate-check
```

Published release archives should include checksums, release provenance, dependency inventory, Go module inventory, build metadata, and third-party license inventory.
