# Receiver workflow example

Copy and adapt `workflow.yaml` in the runtime repository. It is not deployable
as-is: implement the four entrypoints below. Spex does not install this example
in its own `.github/workflows` directory.

See [illustrative implementations of all four entrypoints](entrypoints.md) for
admission/source-fetch control flow, a tooling setup script, receiver execution
and policy examples, output handling, and bounded cleanup.

- `ci/receiver-admit-and-fetch` authenticates GitHub run/artifact metadata,
  authorizes the source, retrieves the request artifact and exact source commit,
  and writes a private admission-context file. Set `context-path` to its location.
  Do not log credentials or trust dispatch values without checks.
- `ci/receiver-setup` installs pinned, trusted tooling. Do not deploy or change the
  test environment here; scenario admission and planning must happen first.
- `ci/receiver-run` reads that admission context and uses the API in `host.go`.
  Supply a real policy with durable claims, a trusted runtime catalog, a
  signal-aware context and an artifact sink. Set `receipt-path` and
  `evidence-directory` outputs when files exist, including after failed tests.
  Return `receiver.ExitCode`.
- `ci/receiver-cleanup` removes additional host-owned resources, tolerates partial
  setup and preserves diagnostics. The example bounds it to two minutes.

For migration-testbench, construct catalog implementations with
`migrationtestbench.New(migrationtestbench.Config{RepositoryRoot: trustedRoot,
SuitePath: trustedSuite, Release: pinnedRelease})`. Keep the submitted source
checkout separate. Configure `receiver.Host.Runtimes` and `DefaultRuntime`
explicitly; submitted definitions cannot register or download implementations.

When the runtime declares shared mutation targets, supply
`receiver.Host.ResourceCoordinator` with a durable store shared by all workers
that can touch them. Request replay claims and resource claims solve different
problems. See [resource coordination](../../docs/resource-claims.md) for backend
scope and recovery after worker loss.

The example separates the receipt artifact from evidence and propagates failed
execution after uploads. Adapt runner, permissions, credentials and timeouts to
the runtime. The sample token and setup steps do not establish an admission policy.

See [the receiver contract](../../docs/receiver.md) for trust boundaries and
qualification limits. `host.go` compiles as an API example; it does not provide
GitHub authentication or a production runtime.
