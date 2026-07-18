# Backend Guarantees and Capability Matrix

This document defines the expected semantics and tested capability differences by backend.

There is no blanket delivery guarantee shared by every backend. Handlers should be idempotent because durable backends may redeliver, while ephemeral backends may lose work at failure boundaries described below.

## Semantics Baseline (All Backends)

- Acceptance and delivery durability are backend-specific.
- Duplicate processing: possible; handlers must be idempotent
- Ordering: not guaranteed unless explicitly documented for a backend/runtime path
- A successful dispatch means the backend-specific acceptance boundary was crossed; it does not imply handler success.
- Unsupported or unproven guarantees must remain explicit rather than being inferred from another backend's tests.

## Capability Matrix (Integration Fixture-Aligned)

The table below reflects capability gates used by `integration/all/integration_scenarios_test.go`. A `Yes` means that fixture currently runs the linked scenario; it is evidence under that fixture's conditions, not by itself a production guarantee across crashes, producer/worker separation, or multiple processes.

| Backend | Backoff | Restart Scenario | Delayed/Retry Restart Scenario | Poison Retry | Dispatch Context Cancel | Fixture No-Dupes | Ordering Scenario | Broker Fault Scenarios | Shutdown During Delay/Retry |
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
| Delay/retry "not before" timing windows | `scenario_retry_delay_timing_windows` parent with `scenario_delay_not_before_window` and `scenario_retry_backoff_not_before_window` (capability-gated) |
| Restart recovery | `scenario_worker_restart_recovery` |
| Delayed/retry restart durability | `scenario_worker_restart_delay_recovery`; `scenario_shutdown_during_delay_retry` |
| Poison retry semantics | `scenario_poison_message_max_retry` |
| Dispatch context cancellation | `scenario_dispatch_context_cancellation` parent with `scenario_dispatch_context_precanceled`, `scenario_dispatch_context_deadline_exceeded`, and `scenario_dispatch_context_followup_health` |
| No-duplicate processing under fixture conditions | `scenario_multi_worker_contention`; `scenario_duplicate_delivery_idempotency`. These do not prove the public `Job.UniqueFor` contract, which remains open in `plan.md`. |
| Ordering contract (suite-level capability) | `scenario_ordering_contract` parent with `scenario_ordering_single_worker_fifo`; concurrent non-guarantee covered by `scenario_ordering_multi_worker_best_effort`; reordering behavior probed by `scenario_ordering_delayed_immediate_mix` and `scenario_ordering_retry_reorder_allowed` |
| Broker fault handling | `scenario_dispatch_during_broker_fault`; `scenario_consume_after_broker_recovery` |
| Shutdown during delay/retry workloads | `scenario_shutdown_during_delay_retry` |
| Pause/Resume capability behavior | `TestObservabilityIntegration_PauseResumeSupport_AllBackends` (`integration/root/observability_integration_test.go`) |
| Native stats capability behavior | `TestObservabilityIntegration_AllBackends` (`integration/root/observability_integration_test.go`) |
| Queue/workflow chain/batch integration baseline | `TestIntegrationQueue_AllBackends` (`integration/all/runtime_integration_test.go`); `TestIntegrationBus_AllBackends` (`integration/bus/integration_test.go`) |
| Workflow failure callback semantics (catch/finally + terminal state) | `TestIntegrationBus_AllBackends` -> `workflow_chain_failure_callbacks` and `workflow_batch_failure_callbacks` (`integration/bus/integration_test.go`) |
| Workflow duplicate callback suppression (SQL runtime/store path) | `TestSQLStore_RuntimeChainFinallyDuplicateCallbackSuppressed`; `TestSQLStore_RuntimeChainCatchAndFinallyDuplicateCallbacksSuppressed`; `TestSQLStore_RuntimeChainFinallyCallbackReplayAfterDispatchFaultSuppressed`; existing batch duplicate callback suppression tests (`integration/bus/callback_sql_integration_test.go`) |
| Workflow dispatch failure state consistency (SQL runtime/store path) | `TestSQLStore_RuntimeChainInitialDispatchFailureStateConsistent`; `TestSQLStore_RuntimeBatchPartialDispatchFailureStateConsistent` (`integration/bus/dispatch_failure_sql_integration_test.go`) |

\* `sqlite` is promoted to full restart/durability coverage in the shared suite when queue and worker use the same test-local DSN (see fixture override logic in `integration/all/integration_scenarios_test.go`).

## Backend Notes

### Redis (`DriverRedis`)

- Uses Asynq-backed runtime semantics.
- An accepted Asynq task is persisted in Redis, but handlers must tolerate redelivery around worker/process failures.
- Same-attempt redelivery for an uncommitted workflow mutation works only before Asynq's final transport attempt in v0.26; its processor archives an exhausted task before consulting the non-failure predicate. This remains an explicit M1-04 blocker in `plan.md`.
- Shared suite treats custom backoff as unsupported in this runtime path (`supportsBackoff=false`).
- Ordering contract is tested in-suite (`supportsOrderingContract=true`) under the current shared scenario's constrained FIFO assumptions.
- Do not generalize this to multi-worker, retry, or delayed/immediate mixed workloads unless explicitly documented and tested.
- Broker fault scenarios are covered in the shared suite (`supportsBrokerFault=true`).

### Database (`DriverDatabase`: MySQL/Postgres/SQLite)

- Acceptance is a committed queue-row insert. Claimed rows use stale-processing recovery, so application handlers must tolerate redelivery if finalization does not commit.
- Supports retry/backoff, poison retry, dispatch context cancellation, and deterministic duplicate prevention in the shared suite.
- Broker fault injection scenarios are not enabled in the shared suite for DB backends.
- DB backends rely on stale-`processing` recovery behavior for crash recovery (`process_recovered` event visibility is important operationally).
- Ordering is not currently claimed in the shared suite for MySQL/Postgres. SQLite ordering is only claimed in the suite under test-local conditions (see matrix note).

### NATS (`DriverNATS`)

- The current implementation uses Core NATS publish/subscribe, not JetStream. It is an ephemeral broker adapter: there is no durable consumer acknowledgement, retained work queue, or crash recovery boundary.
- A successful publish currently means the client accepted the publish call; it is not a durable queue acknowledgement.
- Retry is republish-based. A process or connection failure can lose the original or replacement message, so this backend does not currently conform to a durable committed-retry contract.
- The shared suite exercises core dispatch/processing and backoff/poison behavior only while the fixture remains available.
- Shared suite does not claim restart durability guarantees for delayed/retried work (`supportsRestart=false`, `supportsRestartDelayedDurability=false`).
- Deterministic no-duplicate and ordering guarantees are not claimed in the suite.
- Users should treat ordering as non-guaranteed unless a stronger constrained contract is explicitly added and tested.

### SQS (`DriverSQS`)

- Initial acceptance follows a successful SQS `SendMessage` response. SQS delivery is redeliverable through visibility timeout, so handlers must be idempotent.
- Retry currently republishes before deleting the original. Delete/visibility and poison-message settlement hardening remain tracked in `plan.md`.
- Supports restart recovery in the shared suite, but not delayed/retry restart durability guarantees (`supportsRestartDelayedDurability=false`).
- Broker fault scenarios are not deterministically exercised in the shared suite.
- Ordering guarantees are not claimed.
- Local integration validation uses LocalStack.

### RabbitMQ (`DriverRabbitMQ`)

- Publishing currently does not enable publisher confirms. A successful client publish return is therefore not yet a broker-persistence acknowledgement.
- Worker retry publishes before acknowledging the original, but truthful retry commitment requires a positive publisher confirmation; this remains tracked in `plan.md`.
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
- Do not infer public `UniqueFor` correctness from the fixture no-duplicate column. Public dispatch currently wraps logical jobs with volatile workflow IDs, so logical uniqueness repair remains M1-05 through M1-07 in `plan.md`.
- If you change a capability flag in `integration/all/integration_scenarios_test.go`, update this document in the same PR.
- If you want to claim a stronger backend guarantee publicly, add or unskip the corresponding shared scenario first.

## GA Completion Criteria for This Document

Before GA, ensure this document also includes:

- backend-specific configuration guidance links (timeouts, concurrency, retries)
- any known caveats/limits by backend version
- links to the latest passing shared integration evidence and soak evidence
