# Backend Guarantees and Capability Matrix

This document defines the expected semantics and tested capability differences by backend.

All backends are expected to provide at-least-once delivery semantics. Handlers should be idempotent.

## Semantics Baseline (All Backends)

- Delivery: at-least-once
- Duplicate processing: possible; handlers must be idempotent
- Ordering: not guaranteed unless explicitly documented for a backend/runtime path
- Failure behavior: dispatch/processing errors should be surfaced; no silent loss is acceptable

## Capability Matrix (Integration Fixture-Aligned)

The table below reflects the capability gates used in `integration/all/integration_scenarios_test.go` and the shared scenario suite.
Every capability/guarantee row should be justifiable by a concrete scenario or test. If a cell changes, update the linked scenario references in the same PR.

| Backend | Backoff | Restart Recovery | Delayed/Retry Restart Durability | Poison Retry | Dispatch Context Cancel | Deterministic No-Dupes (suite) | Ordering Contract (suite) | Broker Fault Scenarios | Shutdown During Delay/Retry |
| --- | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| `redis` | No | Yes | Yes | No | No | Yes | Yes | Yes | Yes |
| `mysql` | Yes | Yes | Yes | Yes | Yes | Yes | No | No | Yes |
| `postgres` | Yes | Yes | Yes | Yes | Yes | Yes | No | No | Yes |
| `sqlite` | Yes | Yes* | Yes* | Yes | Yes | Yes | Yes* | No | Yes |
| `nats` | Yes | No | No | Yes | No | No | No | No | No |
| `sqs` | Yes | Yes | No | Yes | Yes | Yes | No | No | No |
| `rabbitmq` | Yes | Yes | Yes | Yes | No | Yes | No | No | Yes |

### Scenario References (Proof Links)

These are the primary shared-scenario proofs for the matrix above.

| Capability / Guarantee | Proving Scenario(s) / Test(s) |
| --- | --- |
| Backoff support / rejection behavior | `scenario_config_option_fuzz`; Redis explicit unsupported path in `TestRedisIntegration_BackoffUnsupported` (`integration/all/integration_scenarios_test.go`) |
| Restart recovery | `scenario_worker_restart_recovery` |
| Delayed/retry restart durability | `scenario_worker_restart_delay_recovery`; `scenario_shutdown_during_delay_retry` |
| Poison retry semantics | `scenario_poison_message_max_retry` |
| Dispatch context cancellation | `scenario_dispatch_context_cancellation` |
| Deterministic no-duplicate processing (suite-level capability) | `scenario_multi_worker_contention`; `scenario_duplicate_delivery_idempotency` |
| Ordering contract (suite-level capability) | `scenario_ordering_contract` parent with `scenario_ordering_single_worker_fifo`; reordering behavior probed by `scenario_ordering_delayed_immediate_mix` and `scenario_ordering_retry_reorder_allowed` |
| Broker fault handling | `scenario_dispatch_during_broker_fault`; `scenario_consume_after_broker_recovery` |
| Shutdown during delay/retry workloads | `scenario_shutdown_during_delay_retry` |
| Pause/Resume capability behavior | `TestObservabilityIntegration_PauseResumeSupport_AllBackends` (`integration/root/observability_integration_test.go`) |
| Native stats capability behavior | `TestObservabilityIntegration_AllBackends` (`integration/root/observability_integration_test.go`) |
| Queue/workflow chain/batch integration baseline | `TestIntegrationQueue_AllBackends` (`integration/all/runtime_integration_test.go`); `TestIntegrationBus_AllBackends` (`integration/bus/integration_test.go`) |

\* `sqlite` is promoted to full restart/durability coverage in the shared suite when queue and worker use the same test-local DSN (see fixture override logic in `integration/all/integration_scenarios_test.go`).

## Backend Notes

### Redis (`DriverRedis`)

- Uses Asynq-backed runtime semantics.
- Shared suite treats custom backoff as unsupported in this runtime path (`supportsBackoff=false`).
- Ordering contract is tested in-suite (`supportsOrderingContract=true`) under the current shared scenario's constrained FIFO assumptions.
- Do not generalize this to multi-worker, retry, or delayed/immediate mixed workloads unless explicitly documented and tested.
- Broker fault scenarios are covered in the shared suite (`supportsBrokerFault=true`).

### Database (`DriverDatabase`: MySQL/Postgres/SQLite)

- Supports retry/backoff, poison retry, dispatch context cancellation, and deterministic duplicate prevention in the shared suite.
- Broker fault injection scenarios are not enabled in the shared suite for DB backends.
- DB backends rely on stale-`processing` recovery behavior for crash recovery (`process_recovered` event visibility is important operationally).
- Ordering is not currently claimed in the shared suite for MySQL/Postgres. SQLite ordering is only claimed in the suite under test-local conditions (see matrix note).

### NATS (`DriverNATS`)

- Supports core dispatch/processing and backoff/poison retry semantics in the shared suite.
- Shared suite does not claim restart durability guarantees for delayed/retried work (`supportsRestart=false`, `supportsRestartDelayedDurability=false`).
- Deterministic no-duplicate and ordering guarantees are not claimed in the suite.
- Users should treat ordering as non-guaranteed unless a stronger constrained contract is explicitly added and tested.

### SQS (`DriverSQS`)

- Supports restart recovery in the shared suite, but not delayed/retry restart durability guarantees (`supportsRestartDelayedDurability=false`).
- Broker fault scenarios are not deterministically exercised in the shared suite.
- Ordering guarantees are not claimed.
- Local integration validation uses LocalStack.

### RabbitMQ (`DriverRabbitMQ`)

- Supports restart recovery and delayed/retry restart durability in the shared suite.
- Broker fault scenarios are not deterministically exercised in the shared suite.
- Ordering guarantees are not claimed.

## Ordering Guarantee Rules (Current Public Position)

Until the shared ordering contract is split into condition-specific scenarios, the safe public posture is:

- Ordering is **not guaranteed by default** across backends.
- Any FIFO behavior observed under a specific backend/test setup should be treated as a constrained implementation detail unless documented here with explicit preconditions.
- Retries, delays, and multi-worker concurrency can reorder execution and should be assumed to do so unless a backend-specific guarantee says otherwise.

## Production Guidance Notes

- Treat this matrix as the contract for what the shared integration suite validates today.
- If you change a capability flag in `integration/all/integration_scenarios_test.go`, update this document in the same PR.
- If you want to claim a stronger backend guarantee publicly, add or unskip the corresponding shared scenario first.

## GA Completion Criteria for This Document

Before GA, ensure this document also includes:

- backend-specific configuration guidance links (timeouts, concurrency, retries)
- any known caveats/limits by backend version
- links to the latest passing shared integration evidence and soak evidence
