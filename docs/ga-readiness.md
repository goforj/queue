# GA Readiness Checklist

This is the release-gate checklist for claiming broad production readiness.

Status as of: 2026-02-22

## How to use this document

- Treat sections 1-5 as `must-pass` for a GA claim.
- Record links to CI runs/artifacts next to each completed item.
- Prefer evidence from CI (`.github/workflows/test.yml`, `.github/workflows/soak.yml`) over local-only runs.

## 1. Functional Correctness Gates (must complete)

- [ ] Unit test suite passes.
  - Command: `go test ./...`
  - Acceptance: green on `main` and release candidate branch.

- [ ] Race test suite passes.
  - Command: `go test -race ./...`
  - Acceptance: green on `main` and release candidate branch.

- [ ] Full integration suite passes across enabled backends.
  - Command: `RUN_INTEGRATION=1 go test -tags=integration ./...`
  - Acceptance: green matrix in CI for `null`, `sync`, `workerpool`, `redis`, `mysql`, `postgres`, `sqlite`, `nats`, `sqs`, `rabbitmq`.

- [ ] Bus integration suite passes across enabled backends.
  - Command: `RUN_INTEGRATION=1 go test -tags=integration ./bus -run TestIntegrationBus_AllBackends -count=1 -v`
  - Acceptance: all enabled backends green (including `rabbitmq`).

## 2. Shared Integration Contract and Recovery Semantics (must complete)

- [ ] `TestIntegrationScenarios_AllBackends` is green across all enabled backends.
  - Command: `RUN_INTEGRATION=1 go test -tags=integration ./... -run '^TestIntegrationScenarios_AllBackends$' -count=1 -v`
  - Acceptance: no unexpected failures; skips only for documented capability gates.

- [ ] Restart and recovery invariants validated on applicable backends.
  - Scenarios:
    - `scenario_worker_restart_recovery`
    - `scenario_worker_restart_delay_recovery`
    - `scenario_shutdown_during_delay_retry`
  - Acceptance: no silent loss, eventual completion, bounded duplicate behavior.

- [ ] Fault-injection / broker recovery scenarios validated on applicable backends.
  - Scenarios:
    - `scenario_dispatch_during_broker_fault`
    - `scenario_consume_after_broker_recovery`
  - Acceptance: failures are surfaced during broker outage and recovery path processes jobs after broker restoration.

- [ ] Duplicate-delivery idempotency scenario remains green.
  - Scenario: `scenario_duplicate_delivery_idempotency`
  - Acceptance: duplicate attempts occur and side-effect commit count remains `1`.

## 3. CI Reliability and Evidence Retention (must complete)

- [ ] Integration CI flakiness is stabilized.
  - Acceptance: 20 consecutive full CI runs on `main` with no infra-related integration failures.
  - Scope: Redis/MySQL/Postgres/SQLite/NATS/SQS/RabbitMQ matrix.

- [ ] Integration/soak/chaos jobs have explicit timeout budgets.
  - Acceptance: `.github/workflows/soak.yml` backend jobs define `timeout-minutes` and logs make timeout source clear.

- [ ] CI stores evidence artifacts for scenario runs.
  - Source: `.github/workflows/soak.yml`
  - Acceptance: per-backend artifacts include raw logs and parsed duration summaries for:
    - `integration-evidence`
    - `integration-soak`
    - `integration-chaos`

- [ ] Duration guardrails are enabled and documented.
  - Sources: `integration_scenarios_test.go`, `scenario_duration_helpers_test.go`, `docs/integration-scenarios.md`
  - Acceptance: threshold regressions fail tests; override precedence is documented and tested.

- [ ] Retry strategy for transient CI bootstrap failures is documented/implemented.
  - Acceptance: known transient testcontainers/bootstrap failures have a clear retry policy in CI docs/workflow.

## 4. Load, Soak, and Performance Validation (must complete)

- [ ] GA target load profile is defined.
  - Acceptance: documented target throughput, P95/P99 latency, queue depth bounds, worker concurrency assumptions, and backend mix.

- [ ] Sustained soak by backend is completed with artifact retention.
  - Minimum target (GA): 1 hour per backend for key production backends (`redis`, `postgres`/`mysql`, `rabbitmq`, `sqs`).
  - Command pattern: `RUN_INTEGRATION=1 RUN_SOAK=1 INTEGRATION_BACKEND=<backend> go test -tags=integration ./... -run '^TestIntegrationScenarios_AllBackends$' -count=1 -v`
  - Acceptance: no hangs, no unbounded backlog growth, no repeated hidden failure churn, evidence artifacts retained.

- [ ] Soak pass/fail thresholds are enforced.
  - Acceptance: soak workflow fails automatically when agreed thresholds are exceeded (latency/error/backlog/duration).
  - Note: scenario duration guardrails exist; expand to soak-specific acceptance thresholds.

- [ ] Worker concurrency behavior is characterized for external backends.
  - Acceptance: throughput/latency guidance exists for `NATS`/`SQS`/`RabbitMQ` using `Workers(count)`.

## 5. Observability, Operations, and Backend Guarantees (must complete)

- [ ] Canonical metrics/events contract is documented.
  - Acceptance: names, labels/fields, and semantics are documented and versioned.
  - Must include recovery/failure events (for example `republish_failed`, `process_recovered`).

- [ ] Alerts and dashboards exist for core operations.
  - Acceptance: at least queue depth, processing latency, failure rate, retry rate, and worker liveness alerts are defined.

- [ ] Runbooks exist for common incidents.
  - Acceptance: in-repo runbooks for backlog growth, broker outage, poison messages, and stuck workers.

- [ ] Backend guarantees are documented explicitly.
  - Acceptance: per backend documentation covers at-least-once semantics, delay/retry durability, ordering, restart behavior, and duplicate expectations.
  - Must match capability-gated behavior in integration fixtures/tests.

- [ ] Production config guidance is documented.
  - Acceptance: recommended settings for concurrency, retries/backoff, DB recovery lease/grace, and backend-specific tuning are documented.

## 6. API/Upgrade Safety (must complete)

- [ ] Compatibility policy is documented (SemVer + deprecation window).
  - Acceptance: explicit compatibility guarantees in public docs.

- [ ] Upgrade/migration regression tests are in place.
  - Acceptance: tests cover common upgrade paths, including DB-backed behavior/schema evolution where applicable.

- [ ] Examples compile and reflect supported API usage.
  - Acceptance: examples compile gate remains green and covers primary production usage patterns.

## 7. Coverage and Test Debt (should complete)

- [ ] Close remaining low-value 0% helper branches where practical.
  - Acceptance: no easy/uncontroversial 0% branches remain in core runtime paths.

- [ ] Add focused tests for remaining weak spots in queue runtime helpers.
  - Acceptance: weak areas in coverage reports have tests or explicit rationale for exclusion.

## Release Decision

GA claim requires all items in sections 1-6 completed and signed off, with links to supporting CI runs/artifacts.
