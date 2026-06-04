# Live Proof

The production proof is a real kind run. It is intentionally separate from normal unit CI because it needs Docker, kind, kubectl, cluster capacity, and image build time.

## What It Proves

- The generator compiles scenario intent into a complete KUTTL workspace.
- KUTTL can create or use a kind cluster.
- Real services are installed through the integration profile.
- Probe Jobs can publish MQTT messages.
- Redpanda offset snapshots and contains assertions work from captured offsets.
- GraphQL assertions work with scenario and correlation IDs.
- The Keycloak variant can obtain and use a client-credentials token.
- Reports, evidence, resource collection, and cleanup work end to end.

## Required Tools

- `docker`
- `kind`
- `kubectl`
- `go`
- `oras` for the OCI bundle proof

The Keycloak proof also needs enough local CPU and memory for Keycloak plus the demo stack.

## Non-Keycloak Proof

```sh
make probe-image
make integration-example-kind
```

Equivalent explicit command:

```sh
PROBE_IMAGE=spex-probe:dev \
PROBE_IMAGE_PULL_POLICY=IfNotPresent \
KUBE_CONTEXT=kind-kind \
INTEGRATION_PROFILE=examples/integration/local-kind-profile.yaml \
WORKSPACE=generated/mqtt-ingestion-basic-live \
scripts/integration_live.sh
```

## Keycloak Proof

```sh
export SPEX_MQTT_USERNAME=tester
export SPEX_MQTT_PASSWORD=tester-password
export SPEX_GRAPHQL_KEYCLOAK_CLIENT_SECRET=graphql-client-secret
export SPEX_KEYCLOAK_ADMIN_PASSWORD=admin-password

make probe-image
make integration-example-kind-keycloak
```

## CI Usage

Use `make live-proof-kind` in a workflow that has Docker and kind available. Use `make live-proof-keycloak` for the stronger Keycloak gate.

Use `make live-proof-oci-bundle` in an environment that has Docker and ORAS available to prove the registry-hosted bundle path. The proof starts a local OCI registry when needed, pushes `examples/bundles/custom-echo`, resolves the immutable digest, then runs `bundle explain`, `bundle lock`, `bundle verify`, and `bundle vendor` against the digest-pinned OCI source. It enables `SPEX_OCI_BUNDLE_PLAIN_HTTP=true` only for that local-registry proof; production registry references should remain HTTPS and digest-pinned.

Normal pull-request CI may run `make production-candidate-check` without the live proof. Release-candidate promotion must include at least one clean live proof for the target integration profile.

## Artifacts

Keep these as CI artifacts:

- generated workspace
- `reports/scenario-run-report.yaml`
- `reports/scenario-run-report.json`
- `evidence/`
- `artifacts/kuttl/`

Failed runs should retain these artifacts even if cleanup succeeds.

Do not commit live-proof output. The generated workspace and KUTTL cluster logs can contain host-local paths, temporary kubeconfig references, Docker plugin paths, and other machine-specific diagnostics. CI artifact collection should exclude `kubeconfig` files unless a short-lived debugging workflow explicitly needs them.
