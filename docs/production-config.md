# Production Configuration Guidance (Baseline)

This document provides baseline configuration guidance for `queue` in production deployments.

Treat these values as starting points. Tune them using workload measurements, integration/soak evidence, and your SLOs.

## General Principles

- Handlers should be idempotent (at-least-once delivery semantics).
- Start with conservative concurrency, then increase while watching:
  - processing latency
  - retry/failure rate
  - backlog depth
  - dependency saturation (DB/API/broker)
- Prefer explicit queue names for critical workloads (`OnQueue("critical")`) and separate worker pools per queue priority.

## Core Queue Runtime Settings

### `Workers(count)`

Use `q.Workers(count)` to control worker concurrency.

Guidance:

- Start with `1-4` workers per process for new services.
- Scale horizontally first if handlers are I/O bound and dependencies are shared.
- Increase per-process workers when:
  - CPU and memory headroom exists
  - dependency limits are not saturating
  - queue backlog grows faster than workers can drain

Watch for:

- rising retries/failures after increasing workers
- downstream rate limiting
- DB connection pool exhaustion (DB backends)

## Job Retry / Backoff Guidance

### Retries (`Job.Retry(n)`)

Use retries for transient failures only.

Starting points:

- network/transient dependency failures: `Retry(3)` to `Retry(5)`
- user/data validation failures: no retry (return terminal failure path)

### Backoff (`Job.Backoff(d)`)

Use backoff to avoid retry storms.

Starting points:

- transient API/DB errors: `250ms` to `2s` backoff
- high-contention jobs: start around `500ms` and tune based on throughput

Notes:

- Backend behavior differs (see `docs/backend-guarantees.md`).
- Shared integration suite capability gates should be treated as the tested contract.

## Backend-Specific Guidance

### Redis (`DriverRedis`)

Good default when:

- you need a mature broker-backed queue with strong operational tooling
- Redis is already part of your stack

Starting points:

- separate queues for latency-sensitive vs bulk jobs
- monitor retry/archive churn and queue depth closely
- validate worker concurrency against Redis CPU/network and downstream dependencies

### MySQL / Postgres / SQLite (`DriverDatabase`)

Good default when:

- you want a durable queue in the same operational footprint as your app DB (MySQL/Postgres)
- local/dev simplicity matters (`sqlite`)

Starting points:

- ensure DB connection pool is sized for worker concurrency + app traffic
- keep worker concurrency modest initially (`1-4` per process)
- monitor query latency and stale-processing recovery events (`process_recovered`)

Important DB recovery knobs (`queue.Config`):

- `DatabaseProcessingRecoveryGrace`
  - grace period before reclaiming stale `processing` jobs
  - start with default unless you have proven false-positive recovery under your handler latencies
- `DatabaseProcessingLeaseNoTimeout`
  - fallback lease for jobs without explicit timeout
  - increase for very long-running jobs if you observe premature stale recovery

When tuning:

- longer leases reduce false-positive recovery
- shorter leases reduce time-to-recovery after worker crashes
- validate changes with crash/restart scenarios and soak runs

### NATS (`DriverNATS`)

Good default when:

- you already operate NATS and want lightweight broker integration

Starting points:

- use conservative concurrency while validating duplicate/ordering expectations
- confirm delayed/retry durability expectations against your workload (see capability matrix)

### SQS (`DriverSQS`)

Good default when:

- you are on AWS and want managed queue infrastructure

Starting points:

- partition critical and bulk jobs into separate queues
- validate handler duration vs SQS visibility timeout behavior in your environment
- monitor duplicate deliveries and end-to-end latency under retries

Operational note:

- Local integration tests use LocalStack; production behavior must still be validated in AWS.

### RabbitMQ (`DriverRabbitMQ`)

Good default when:

- RabbitMQ is already a first-class platform dependency

Starting points:

- separate queues by priority/workload class
- validate restart/retry behavior and throughput under your expected publish/consume rate
- watch connection/channel health and reconnect churn

## Queue Layout Recommendations

Use multiple queues when workloads differ materially by:

- latency sensitivity
- expected runtime
- retry behavior
- dependency target (for blast-radius isolation)

Example layout:

- `critical`
- `default`
- `bulk`

Run dedicated workers (or worker pools) per queue class when needed.

## Observability Hooks (Enable Early)

At minimum, wire:

- runtime observer (`queue.Config.Observer`)
- workflow observer (`queue.WithObserver(...)`) when using chains/batches/callbacks

Track and alert on:

- `process_failed`
- `process_retried`
- `republish_failed`
- `process_recovered` (DB backends)

See:

- `docs/metrics-contract.md`
- `docs/ops-alerts.md`
- `docs/runbooks/`

## Rollout Checklist (Per Service)

- Start with low concurrency and one or two queues
- Enable observers and dashboards before production traffic
- Run a canary deployment
- Verify backlog, latency, retry rate, and duplicate behavior
- Increase concurrency gradually
- Record final chosen values and rationale

## GA Completion Criteria for This Document

Before GA, expand this baseline with:

- backend-specific recommended ranges derived from soak/benchmark evidence
- SQS visibility timeout guidance with concrete examples
- DB connection pool sizing examples tied to worker concurrency
- links to production dashboards and runbooks
