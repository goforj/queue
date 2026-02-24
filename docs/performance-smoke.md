# Performance Smoke Guardrails

These checks are lightweight regression alarms for release hardening. They are not optimization targets and are intentionally looser than local benchmark results.

## Scope

Current smoke guardrails cover:

- enqueue throughput sanity (`BenchmarkEnqueueSync_NoObserver`)
- enqueue throughput with observability overhead (`BenchmarkEnqueueSync_WithObserver`)
- worker lifecycle latency sanity (`BenchmarkWorkerpoolLifecycle`)

They are run in CI from `.github/workflows/soak.yml` via `scripts/check-bench-smoke.sh`.

## Philosophy

- Use thresholds to catch catastrophic regressions.
- Do not gate PRs on noisy microbench comparisons.
- Review benchmark smoke results during release candidate validation.

## Threshold configuration

`scripts/check-bench-smoke.sh` supports environment overrides:

- `BENCH_SMOKE_MAX_NS_OP_ENQUEUE_SYNC_NO_OBSERVER`
- `BENCH_SMOKE_MAX_NS_OP_ENQUEUE_SYNC_WITH_OBSERVER`
- `BENCH_SMOKE_MAX_NS_OP_WORKERPOOL_LIFECYCLE`

Thresholds are expressed in `ns/op`.

## Release review

For release candidates, review the `benchmark-smoke` job summary and attached artifacts (`bench-smoke.log`, `bench-smoke-summary.md`) alongside integration flake evidence.
