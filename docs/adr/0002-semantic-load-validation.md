# ADR 0002: Semantic Load Validation

Status: accepted

## Context

spex is a deterministic scenario compiler and evidence collector. It is good at proving that a scenario produced the expected cross-system behavior. It is not intended to compete with load-generation tools such as k6, JMeter, Gatling, or Locust.

Classic load tools are optimized for traffic volume, load profiles, latency percentiles, dashboards, and distributed workers. spex's strength is different: correctness validation across system boundaries.

## Decision

spex supports semantic load validation, not generic load testing.

The product position is:

```text
spex validates scenario correctness under load conditions.
```

The first suite-level contract is:

```yaml
spec:
  execution:
    repetitions: 100
    concurrency: 10
    rateLimit:
      perSecond: 25
    failFast: false
    maxFailures: 10
```

These controls describe bounded scenario repetition and failure aggregation for future runner behavior. Existing single-run suite behavior remains the default when `spec.execution` is omitted.

## Non-Goals

This decision does not add:

- distributed load agents
- a load-profile DSL
- percentile latency dashboards
- benchmark scoring
- generic traffic-generation frameworks
- provider-owned execution loops

External tools can generate load. spex remains responsible for deterministic scenario execution, cross-system assertions, evidence, and reports.

## Design Rules

- Each repeated execution must get a deterministic iteration identity.
- Iteration identity must be usable for correlation IDs, evidence paths, and reports.
- Failure aggregation must preserve enough evidence to debug representative failures.
- Load controls must remain bounded and reproducible.
- The runner must not hide correctness failures behind aggregate success metrics.
- Generated reports should distinguish semantic pass/fail from performance measurements.

## Current Milestone

The first implementation adds a validated `spec.execution` contract to scenario suites and surfaces it in suite planning and explanation output.

Runner scheduling remains unchanged until the next milestone.

## Follow-Up Work

- Generate deterministic iteration IDs for repeated suite execution.
- Add bounded repetition and concurrency to `suite run`.
- Add failure aggregation fields to suite reports.
- Add optional rate limiting after repetition/concurrency behavior is proven.
