# Reusable-workflow qualification

The implementation adds prepare-only Action authoring, same-run admission and
trusted runtime staging. Reusable and dispatched requests then enter the existing
runtime execution path. Migration-testbench supplies configuration, a shared
Action and a reusable workflow; request processing remains in Spex.

| Check | Result | Evidence |
| --- | --- | --- |
| Action dispatch compatibility and prepare mode | PASS | 85 local tests with the candidate executable; Ruff and Pyright pass |
| Runtime source admission | PASS | Existing source-policy tests plus caller repository/run/commit/event/actor checks |
| Trusted runtime staging | PASS | Private copies, stable identities, executable permissions, rejected symlinks, failure cleanup and cancellation |
| Spex quality gates | PASS | Full root and HTTP-bundle race tests, vet and module verification |
| Spex security gates | PASS | No reachable vulnerability findings; imported/module advisories remain reported |
| Existing testbench qualification | PASS | 47 tests pass; race, vet and module verification pass |
| Reusable workflow syntax | PASS | actionlint v1.7.7 |
| Inline MongoDB planning | PASS | Candidate stages the actual testbench and plans an inline Gherkin smoke test with the inherited catalog and binding |
| Runtime-owned baseline planning | PASS | Candidate stages the gateway profile and plans only the existing baseline without commit-tag selection |
| Published reusable-capable Spex | PASS | rc.46 includes these commands; the runtime's shared Action still needs its pin updated |
| Hosted shared Action and reusable workflow | BLOCKED | Publish changes and enable private Action sharing; pin both interfaces to the same reviewed release |
| Inline MongoDB execution on GitHub | BLOCKED | Needs the published components, caller permissions, runner and ECR OIDC access |
| Baseline execution through the reusable workflow | BLOCKED | Needs hosted qualification with private chart/image pins and required fixture secrets |

Spex rc.46 was published on 2026-10-14 from commit
`b3415312876f8e75de3fcd0a23263e20fe6833fd`. Local and hosted security,
race, static-analysis and production-candidate gates passed. Downloaded Linux
amd64 and HTTP-bundle archive checksums match the release assets; version and
provenance records identify the release commit.

No remote dispatch, production deployment or application-release workflow change
occurred. Existing testbench rc.43 entrypoints retain their pins and behavior.
The preview shared Action still pins rc.45 and rejects it before staging because
reusable admission is unavailable there. Update that host-owned pin to rc.46
before attempting hosted tests.

The caller's `GITHUB_TOKEN` reads its own request artifact and source. Contents
write permits the existing durable replay-claim mechanism in the caller repo.
The runtime repository still controls catalogs, profiles and guardrails. The
reusable workflow's source context belongs to the caller; a content identity
records the separately staged trusted runtime files.
