# Scenario Repository Guide

A production scenario repository owns test intent and CI configuration. It should not own generated KUTTL.

## Recommended Layout

```text
suite.yaml
scenarios/
features/
queries/
catalogs/
bindings/
integration/
generated/
reports/
ci/
.github/workflows/
```

Use local files for small teams. Use external Git refs when bindings, integration profiles, or catalogs are owned by a platform team.

## Tester-Owned Files

- `suite.yaml`
- `scenarios/*.yaml`
- `features/*.feature`
- `queries/*.graphql`

## Platform-Owned Files

- `bindings/*.yaml`
- `integration/*.yaml`
- Helm values
- kind config
- target namespace secrets
- shared catalogs

## External References

Local checked-out platform repo:

```yaml
spec:
  bindingRef: ../../platform-targets/bindings/dev.yaml
  integrationProfileRef: ../../platform-targets/integration/local-kind.yaml
```

Explicit Git ref:

```yaml
spec:
  bindingRef: git::https://github.com/pruefwerk/platform-targets.git//bindings/dev.yaml@v1.2.3
  integrationProfileRef: git::https://github.com/pruefwerk/platform-targets.git//integration/local-kind.yaml@v1.2.3
```

GitHub Actions-style shorthand:

```yaml
spec:
  bindingRef: team/platform-targets/bindings/dev.yaml@v1.2.3
```

Set `SPEX_GIT_REF_BASE_URL` for GitHub Enterprise.

Structured scenario entries can override the default binding, integration profile, scenario parameters, or tags for one path or glob:

```yaml
spec:
  scenarios:
    - path: features/**/*.feature
      tags: [smoke]
    - path: features/keycloak.feature
      bindingRef: git::https://github.com/pruefwerk/platform-targets.git//bindings/keycloak.yaml@v1.2.3
      integrationProfileRef: git::https://github.com/pruefwerk/platform-targets.git//integration/keycloak-kind.yaml@v1.2.3
      parameters:
        tenantId: tenant-dev
```

## CI Commands

Non-cluster validation:

```sh
spex suite validate --suite suite.yaml
spex suite plan --suite suite.yaml --format json
spex suite explain --suite suite.yaml --format json
spex catalog check --suite suite.yaml --format json
spex catalog docs --suite suite.yaml --out reports/catalog.md
spex suite compile --suite suite.yaml --out generated/ci --run-id ci
```

Production gate:

```sh
spex doctor \
  --suite suite.yaml \
  --skip-host-tools \
  --require-pinned-git-refs \
  --require-pinned-images \
  --scan-artifacts reports \
  --scan-artifacts generated/ci \
  --format json
```

The artifact scan also fails if a file named `kubeconfig`, a file ending in `.kubeconfig`, or kubeconfig-shaped content is present, so live workflows should delete generated kubeconfigs before upload.

Live proof:

```sh
spex suite run --suite suite.yaml --out generated/live --run-id live --collect-resource-usage
```

## Gherkin

Gherkin is useful after the catalog is stable. Step text maps to catalog expressions, not compiled Go code. This lets users extend vocabulary by editing catalog YAML instead of recompiling the binary.

Keep Gherkin constrained:

- no free-form imperative steps
- `Background` is allowed for shared setup
- multiple `Scenario` blocks per feature file are allowed
- tags are allowed on features, scenarios, and scenario outlines
- `Scenario Outline` with one `Examples` table is allowed
- one meaning per expression
- catalog review required for new shared expressions
- generated ScenarioModel remains the source of truth
