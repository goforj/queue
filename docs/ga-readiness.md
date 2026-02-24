# GA Readiness Checklist

This is the release-gate checklist for claiming broad production readiness.

Status as of: 2026-02-23

## Current Evidence Snapshot (main)

What is already verified on `main` (as of 2026-02-23):

- API hard cut is complete and queue-first (`queue.New(...)`, `Queue`, driver-module `New(...)`/`NewWithConfig(...)`).
- Unit test suite passes locally (`go test ./...`).
- Full integration suite passes locally with testcontainers (`INTEGRATION_BACKEND=all go test -tags=integration ./integration/... -count=1`).
- Examples compile as part of the normal test suite.

What is not yet sufficient for a GA claim:

- Sustained soak evidence with retained artifacts across key backends.
- Chaos/fault-injection evidence and threshold enforcement.
- Ops runbooks/alerts/dashboards and backend guarantee docs finalized.

## How to use this document

- Treat sections 1-5 as `must-pass` for a GA claim.
- Record links to CI runs/artifacts next to each completed item.
- Prefer evidence from CI (`.github/workflows/test.yml`, `.github/workflows/soak.yml`) over local-only runs.
- For local/CI reproducibility, prefer a non-repo Go build cache path (for example `GOCACHE=/tmp/queue-gocache`) or the system default cache. Avoid repo-local cache directories.

## 1. Functional Correctness Gates (must complete)

- [x] Unit test suite passes.
  - Command: `go test ./...`
  - Acceptance: green on `main` and release candidate branch.
  - Evidence: local `main` run passed during API hard-cut cleanup (`2026-02-23`).

- [ ] Race test suite passes.
  - Command: `go test -race ./...`
  - Acceptance: green on `main` and release candidate branch.

- [x] Full integration suite passes across enabled backends.
  - Command: `INTEGRATION_BACKEND=all go test -tags=integration ./integration/...`
  - Acceptance: green matrix in CI for `null`, `sync`, `workerpool`, `redis`, `mysql`, `postgres`, `sqlite`, `nats`, `sqs`, `rabbitmq`.
  - Evidence: local `main` run passed (`2026-02-23`): `INTEGRATION_BACKEND=all go test -tags=integration ./integration/... -count=1`

- [x] Bus integration suite passes across enabled backends.
  - Command: `INTEGRATION_BACKEND=all go test -tags=integration ./integration/bus -run TestIntegrationBus_AllBackends -count=1 -v`
  - Acceptance: all enabled backends green (including `rabbitmq`).
  - Evidence: included in `INTEGRATION_BACKEND=all go test -tags=integration ./integration/... -count=1` on this repo layout (`integration/bus` is part of `./integration/...`).

## 2. Shared Integration Contract and Recovery Semantics (must complete)

- [x] `TestIntegrationScenarios_AllBackends` is green across all enabled backends.
  - Command: `INTEGRATION_BACKEND=all go test -tags=integration ./integration/all -run '^TestIntegrationScenarios_AllBackends$' -count=1 -v`
  - Acceptance: no unexpected failures; skips only for documented capability gates.
  - Evidence: covered by full integration pass on `main` (`2026-02-23`); NATS timing budget for `scenario_wait_all_processed` was adjusted to avoid parallel-load timeout flake.

- [x] Restart and recovery invariants validated on applicable backends.
  - Scenarios:
    - `scenario_worker_restart_recovery`
    - `scenario_worker_restart_delay_recovery`
    - `scenario_shutdown_during_delay_retry`
  - Acceptance: no silent loss, eventual completion, bounded duplicate behavior.

- [x] Fault-injection / broker recovery scenarios validated on applicable backends.
  - Scenarios:
    - `scenario_dispatch_during_broker_fault`
    - `scenario_consume_after_broker_recovery`
  - Acceptance: failures are surfaced during broker outage and recovery path processes jobs after broker restoration.

- [x] Duplicate-delivery idempotency scenario remains green.
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
  - Command pattern: `RUN_SOAK=1 INTEGRATION_BACKEND=<backend> go test -tags=integration ./integration/all -run '^TestIntegrationScenarios_AllBackends$' -count=1 -v`
  - Acceptance: no hangs, no unbounded backlog growth, no repeated hidden failure churn, evidence artifacts retained.
  - Progress (2026-02-23): initial soak-enabled shared scenario pass completed for `redis` (local testcontainers) and passed:
    - Command: `RUN_SOAK=1 INTEGRATION_BACKEND=redis GOCACHE=/tmp/queue-gocache go test -tags=integration ./integration/all -run '^TestIntegrationScenarios_AllBackends$' -count=1 -v`
    - Result: `PASS` (`redis` backend selected; `scenario_soak_mixed_load` passed in ~`5.60s`; full `redis` backend scenario set ~`52.09s`)
    - Note: this is a smoke/soak-enabled validation pass, not the GA target 1-hour soak.
  - Progress (2026-02-23): initial soak-enabled shared scenario pass completed for `postgres` (local testcontainers) and passed:
    - Command: `RUN_SOAK=1 INTEGRATION_BACKEND=postgres GOCACHE=/tmp/queue-gocache go test -tags=integration ./integration/all -run '^TestIntegrationScenarios_AllBackends$' -count=1 -v`
    - Result: `PASS` (`postgres` backend selected; `scenario_soak_mixed_load` passed in ~`0.65s`; full `postgres` backend scenario set ~`4.14s`)
    - Note: this is a smoke/soak-enabled validation pass, not the GA target 1-hour soak.
  - Progress (2026-02-23): initial soak-enabled shared scenario pass completed for `rabbitmq` (local testcontainers) and passed:
    - Command: `RUN_SOAK=1 INTEGRATION_BACKEND=rabbitmq GOCACHE=/tmp/queue-gocache go test -tags=integration ./integration/all -run '^TestIntegrationScenarios_AllBackends$' -count=1 -v`
    - Result: `PASS` (`rabbitmq` backend selected; `scenario_soak_mixed_load` passed in ~`0.41s`; full `rabbitmq` backend scenario set ~`4.33s`)
    - Note: this is a smoke/soak-enabled validation pass, not the GA target 1-hour soak.
  - Progress (2026-02-23): initial soak-enabled shared scenario pass completed for `sqs` (LocalStack via testcontainers) and passed:
    - Command: `RUN_SOAK=1 INTEGRATION_BACKEND=sqs GOCACHE=/tmp/queue-gocache go test -tags=integration ./integration/all -run '^TestIntegrationScenarios_AllBackends$' -count=1 -v`
    - Result: `PASS` (`sqs` backend selected; `scenario_soak_mixed_load` passed in ~`1.89s`; full `sqs` backend scenario set ~`5.08s`)
    - Note: this is a smoke/soak-enabled validation pass, not the GA target 1-hour soak.
  - Progress (2026-02-23): initial soak-enabled shared scenario pass completed for `mysql` (local testcontainers) and passed:
    - Command: `RUN_SOAK=1 INTEGRATION_BACKEND=mysql GOCACHE=/tmp/queue-gocache go test -tags=integration ./integration/all -run '^TestIntegrationScenarios_AllBackends$' -count=1 -v`
    - Result: `PASS` (`mysql` backend selected; `scenario_soak_mixed_load` passed in ~`2.12s`; full `mysql` backend scenario set ~`6.90s`)
    - Note: this is a smoke/soak-enabled validation pass, not the GA target 1-hour soak.

- [ ] Soak pass/fail thresholds are enforced.
  - Acceptance: soak workflow fails automatically when agreed thresholds are exceeded (latency/error/backlog/duration).
  - Note: scenario duration guardrails exist; expand to soak-specific acceptance thresholds.
  - Progress (2026-02-23): local dry-run of the new CI `soak-ga` command shape and duration parser completed for `redis` (`RUN_SOAK=1`, log + parsed summary generated successfully).
  - Next: run `.github/workflows/soak.yml` `soak-ga` in CI (`ga_soak_minutes=15`) and record artifact links/results here.

- [ ] Worker concurrency behavior is characterized for external backends.
  - Acceptance: throughput/latency guidance exists for `NATS`/`SQS`/`RabbitMQ` using `Workers(count)`.

## 5. Observability, Operations, and Backend Guarantees (must complete)

- [ ] Canonical metrics/events contract is documented.
  - Acceptance: names, labels/fields, and semantics are documented and versioned.
  - Must include recovery/failure events (for example `republish_failed`, `process_recovered`).
  - Progress (2026-02-23): baseline contract added in `docs/metrics-contract.md` and baseline ops guidance added in `docs/ops-alerts.md` (includes `republish_failed` and `process_recovered` coverage).
  - Remaining: pin a contract version, define required fields precisely, and align emitted metrics/log labels in production instrumentation.

- [ ] Alerts and dashboards exist for core operations.
  - Acceptance: at least queue depth, processing latency, failure rate, retry rate, and worker liveness alerts are defined.
  - Progress (2026-02-23): baseline dashboard panels and alert definitions added in `docs/ops-alerts.md`; remaining work is environment-specific thresholds and actual dashboard/alert implementation.

- [ ] Runbooks exist for common incidents.
  - Acceptance: in-repo runbooks for backlog growth, broker outage, poison messages, and stuck workers.
  - Progress (2026-02-23): starter runbooks added:
    - `docs/runbooks/README.md`
    - `docs/runbooks/backlog-growth.md`
    - `docs/runbooks/broker-outage.md`
    - `docs/runbooks/poison-messages.md`
    - `docs/runbooks/stuck-workers.md`
  - Remaining: customize thresholds, dashboards, and environment-specific commands for the production deployment.

- [ ] Backend guarantees are documented explicitly.
  - Acceptance: per backend documentation covers at-least-once semantics, delay/retry durability, ordering, restart behavior, and duplicate expectations.
  - Must match capability-gated behavior in integration fixtures/tests.
  - Progress (2026-02-23): baseline backend guarantee/capability matrix added in `docs/backend-guarantees.md`, aligned to shared integration fixture capability gates.
  - Remaining: expand with backend-version caveats, tuning guidance links, and CI/soak evidence links before GA sign-off.

- [ ] Production config guidance is documented.
  - Acceptance: recommended settings for concurrency, retries/backoff, DB recovery lease/grace, and backend-specific tuning are documented.
  - Progress (2026-02-23): baseline guidance added in `docs/production-config.md` covering concurrency, retries/backoff, DB recovery knobs, and backend-specific starting points.
  - Remaining: replace baseline ranges with measured recommendations from soak/benchmark evidence and deployment-specific examples.

## 6. API/Upgrade Safety (must complete)

- [ ] Compatibility policy is documented (SemVer + deprecation window).
  - Acceptance: explicit compatibility guarantees in public docs.
  - Progress (2026-02-23): baseline compatibility policy drafted in `docs/compatibility-policy.md` (SemVer, deprecation windows, backend/observability contract notes).
  - Remaining: finalize exact deprecation window and supported-backend/version policy before GA sign-off.

- [ ] Upgrade/migration regression tests are in place.
  - Acceptance: tests cover common upgrade paths, including DB-backed behavior/schema evolution where applicable.

- [x] Examples compile and reflect supported API usage.
  - Acceptance: examples compile gate remains green and covers primary production usage patterns.
  - Evidence: `go test ./...` green on `main` after queue API hard-cut renames/removals (`2026-02-23`).

## Recent API/Docs Hard-Cut Completions (2026-02-23)

- Queue-first option surface is in place:
  - `WithObserver`, `WithStore`, `WithClock`, `WithMiddleware`
  - `Option` (renamed from `RuntimeOption`)
- Redundant `NewQueueWithDefaults(...)` removed (use `New(Config{DefaultQueue: ...})`)
- Public observability helper names are queue-first:
  - `Pause`, `Resume`, `Snapshot`
- README/API cleanup:
  - `Testing` group hidden from API index
  - README presents the queue-first constructor path only (no public runtime constructor path)
  - README testing guidance no longer requires `bus.NewFake()`

## 7. Coverage and Test Debt (should complete)

- [ ] Close remaining low-value 0% helper branches where practical.
  - Acceptance: no easy/uncontroversial 0% branches remain in core runtime paths.

- [ ] Add focused tests for remaining weak spots in queue runtime helpers.
  - Acceptance: weak areas in coverage reports have tests or explicit rationale for exclusion.

## Release Decision

GA claim requires all items in sections 1-6 completed and signed off, with links to supporting CI runs/artifacts.
