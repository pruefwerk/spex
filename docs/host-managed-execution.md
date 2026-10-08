# Host-managed execution

Some environments require a trusted runtime host to admit an execution, assign
ownership and coordinate shared resources. Declare that requirement in the suite:

```yaml
spec:
  execution:
    requireHost: true
```

This field does not forbid shared environments. It requires the environment owner
to control execution admission. Integration profiles still describe real setup;
do not replace them with a setup command that deliberately fails.

`suite validate`, `suite list` and `suite plan` remain available for inspection.
Direct `suite run`, `suite compile` and standalone scenario-runtime execution
reject this requirement before exporting runnable workspaces or starting setup.
There is no CLI flag, environment-variable override or scenario overlay that
grants host authority. Existing suites inherit `requireHost: false` and retain
their behavior.

Trusted embedding code uses `migrationtestbench.New` to instantiate the receiver
runtime. That API marks the execution path as host-managed; it does not implement
the host's admission policy or prove its environment safe. The host must authorize
the source and selected operations, supply resource contracts and coordination
where needed, own temporary resources, verify release postconditions, and report
retained effects. Use Spex's receiver policy and resource coordinator interfaces
for those responsibilities. An unqualified host must not expose the runtime.

The requirement remains in resolved suite configuration; a host does not erase it.
Only trusted host code can supply this authority. Directly authoring different
suites or using lower-level probe commands is outside this suite's admission
contract: Kubernetes and service permissions must independently restrict access.

This mechanism is newer than `v0.1.0-rc.39`. Consumers need a release containing
it before merging suite definitions that use `requireHost`. Earlier releases
reject the unknown field instead of executing the suite.
