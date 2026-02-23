# Backend Guarantees and Capability Matrix

This document defines the expected semantics and tested capability differences by backend.

All backends are expected to provide at-least-once delivery semantics. Handlers should be idempotent.

## Semantics Baseline (All Backends)

- Delivery: at-least-once
- Duplicate processing: possible; handlers must be idempotent
- Ordering: not guaranteed unless explicitly documented for a backend/runtime path
- Failure behavior: dispatch/processing errors should be surfaced; no silent loss is acceptable

## Capability Matrix (Integration Fixture-Aligned)

The table below reflects the capability gates used in `integration_scenarios_test.go` and the shared scenario suite.

| Backend | Backoff | Restart Recovery | Delayed/Retry Restart Durability | Poison Retry | Dispatch Context Cancel | Deterministic No-Dupes (suite) | Ordering Contract (suite) | Broker Fault Scenarios | Shutdown During Delay/Retry |
| --- | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| `redis` | No | Yes | Yes | No | No | Yes | Yes | Yes | Yes |
| `mysql` | Yes | Yes | Yes | Yes | Yes | Yes | No | No | Yes |
| `postgres` | Yes | Yes | Yes | Yes | Yes | Yes | No | No | Yes |
| `sqlite` | Yes | Yes* | Yes* | Yes | Yes | Yes | Yes* | No | Yes |
| `nats` | Yes | No | No | Yes | No | No | No | No | No |
| `sqs` | Yes | Yes | No | Yes | Yes | Yes | No | No | No |
| `rabbitmq` | Yes | Yes | Yes | Yes | No | Yes | No | No | Yes |

\* `sqlite` is promoted to full restart/durability coverage in the shared suite when queue and worker use the same test-local DSN (see fixture override logic in `integration_scenarios_test.go`).

## Backend Notes

### Redis (`DriverRedis`)

- Uses Asynq-backed runtime semantics.
- Shared suite treats custom backoff as unsupported in this runtime path (`supportsBackoff=false`).
- Ordering contract is tested in-suite (`supportsOrderingContract=true`).
- Broker fault scenarios are covered in the shared suite (`supportsBrokerFault=true`).

### Database (`DriverDatabase`: MySQL/Postgres/SQLite)

- Supports retry/backoff, poison retry, dispatch context cancellation, and deterministic duplicate prevention in the shared suite.
- Broker fault injection scenarios are not enabled in the shared suite for DB backends.
- DB backends rely on stale-`processing` recovery behavior for crash recovery (`process_recovered` event visibility is important operationally).

### NATS (`DriverNATS`)

- Supports core dispatch/processing and backoff/poison retry semantics in the shared suite.
- Shared suite does not claim restart durability guarantees for delayed/retried work (`supportsRestart=false`, `supportsRestartDelayedDurability=false`).
- Deterministic no-duplicate and ordering guarantees are not claimed in the suite.

### SQS (`DriverSQS`)

- Supports restart recovery in the shared suite, but not delayed/retry restart durability guarantees (`supportsRestartDelayedDurability=false`).
- Broker fault scenarios are not deterministically exercised in the shared suite.
- Ordering guarantees are not claimed.
- Local integration validation uses LocalStack.

### RabbitMQ (`DriverRabbitMQ`)

- Supports restart recovery and delayed/retry restart durability in the shared suite.
- Broker fault scenarios are not deterministically exercised in the shared suite.
- Ordering guarantees are not claimed.

## Production Guidance Notes

- Treat this matrix as the contract for what the shared integration suite validates today.
- If you change a capability flag in `integration_scenarios_test.go`, update this document in the same PR.
- If you want to claim a stronger backend guarantee publicly, add or unskip the corresponding shared scenario first.

## GA Completion Criteria for This Document

Before GA, ensure this document also includes:

- backend-specific configuration guidance links (timeouts, concurrency, retries)
- any known caveats/limits by backend version
- links to the latest passing shared integration evidence and soak evidence
