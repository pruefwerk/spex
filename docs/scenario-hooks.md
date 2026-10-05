# Before-scenario hooks

Run a trusted project executable before every selected scenario:

```sh
spex suite run --suite suite.yaml --before-scenario-hook /absolute/path/to/hook
```

Spex provides `SPEX_SCENARIO_WORKSPACE` as an absolute path. The executable writes
one JSON object to stdout, mapping environment names to string values:

```json
{"EXAMPLE_RUNTIME_TOKEN":"fresh-value"}
```

Spex passes these overrides to the scenario's KUTTL process, setup commands,
evidence collectors, and cleanup commands. It does not change the parent
environment. Parallel scenarios receive independent environments. Hooks run
after rate-limit waits and before execution, including repeated scenarios.
Without a hook, execution stays unchanged.

The default hook deadline is two minutes. Override it with
`--before-scenario-hook-timeout`, up to ten minutes. Hook stdout has a 64 KiB
limit. Invalid output, timeout, or process failure prevents that scenario from
running and produces a `before_scenario_hook_failed` report. Suite failure policy
still determines whether later scenarios run.

Spex captures hook output privately and does not write its environment values to
generated files or reports. It suppresses hook stderr because authentication
errors may contain credentials. Treat the hook as trusted code and do not print
credentials from subsequent scenario commands. Running the hook directly in a
workflow shell would expose its JSON output.

Keep provider-specific authentication in the project. Spex does not implement
GitHub OIDC or AWS role assumptions. Renewal occurs between scenarios, not during
a scenario: each session must cover that scenario's setup, execution, evidence
collection, and cleanup.
