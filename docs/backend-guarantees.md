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
| No-duplicate processing under fixture conditions | `scenario_multi_worker_contention`; `scenario_duplicate_delivery_idempotency`. Public logical `Job.UniqueFor` behavior is separately exercised through `TestIntegrationQueue_AllBackends` in `integration/all/runtime_integration_test.go`. |
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

## `UniqueFor` Identity and Scope

Every driver receives one versioned logical identity composed from:

- the effective physical queue name;
- the logical application job type; and
- the canonical serialized logical payload bytes.

Absent payloads, zero-byte payloads, and the exact JSON `null` payload share one canonical absence identity so removing the legacy workflow envelope cannot change a job's key. All other payload bytes remain exact. Generated dispatch, job, chain, and batch IDs are correlation metadata and do not affect duplicate suppression. Retry, delay, timeout, and backoff options are also excluded. This lets two independently constructed workflow envelopes suppress the same logical work without conflating observability IDs with delivery policy.

| Backend | Claim Scope | Failure Boundary |
| --- | --- | --- |
| `null`, `sync`, `workerpool` | One queue runtime instance | Claims live in memory and disappear when that runtime exits. Known pre-acceptance failures release their claim. |
| `mysql`, `postgres`, `sqlite` | All producers sharing the database | The uniqueness claims and queue row commit in one database transaction. |
| `redis` | All producers sharing Redis | TTL must be at least one second. A token-owned Redis claim is released after a definite Asynq physical duplicate. Other enqueue errors are ambiguous because Redis may have committed before the response was lost, so the claim remains until TTL to fail closed against duplicate retries. A producer crash before Asynq acceptance can therefore suppress work until the TTL expires. |
| `nats`, `sqs`, `rabbitmq` | One queue runtime instance | Claims live in memory and disappear when that runtime exits. Failures known to occur before publication release their claim; ambiguous server roundtrip, send, or confirmation failures retain it until TTL to avoid admitting an immediate duplicate. |

Claims use a fixed acquisition-time TTL; successful processing does not shorten the window. Canonical SQL keys deliberately do not perpetuate the old delimiter-based key because doing so would retain its collisions and double high-cardinality lock rows forever. Redis retains Asynq's physical claim alongside the canonical claim for direct-job compatibility. Older public workflow producers embedded volatile IDs in their physical payload, so every public workflow producer must be upgraded in one coordinated cutover. SQL deployments must either quiesce unique dispatches for the largest prior TTL before resuming or explicitly accept a transient mixed-version duplicate window.

## Backend Notes

### Local runtimes (`DriverSync`, `DriverWorkerpool`)

- Sync executes work inline. Workerpool executes with the concurrency configured by `WithWorkers`; when no explicit buffer is supplied, its queue capacity scales with that worker count.
- Once shutdown begins, new public work is rejected. Workflow continuations are admitted only through a runtime-scoped permit held by an active handler; that permit expires when the handler returns and cannot authorize another runtime.
- Accepted delayed work and callback descendants remain part of the drain. A shutdown deadline bounds the current attempt; if it expires, shutdown returns the context error and a later call can continue cleanup.

### Redis (`DriverRedis`)

- Uses Asynq-backed runtime semantics.
- An accepted Asynq task is persisted in Redis, but handlers must tolerate redelivery around worker/process failures.
- Asynq v0.26 archives an exhausted task before consulting its non-failure predicate. New tasks with an explicit retry budget carry one header-marked transport reserve: workers expose the original application budget, explicitly archive terminal application errors, and reuse the reserve for uncommitted workflow or lease-recovery redelivery. Deploy workers before producers; already-queued legacy tasks remain subject to the upstream final-attempt behavior.
- Public workflow dispatch now preserves its zero retry policy instead of allowing Asynq to substitute its default of 25 retries. Applications that intentionally relied on that old fallback must set `.Retry(25)` explicitly. This is a runtime-behavior migration, separate from the worker-first transport-reserve rollout above.
- Shared suite treats custom backoff as unsupported in this runtime path (`supportsBackoff=false`).
- Ordering contract is tested in-suite (`supportsOrderingContract=true`) under the current shared scenario's constrained FIFO assumptions.
- Do not generalize this to multi-worker, retry, or delayed/immediate mixed workloads unless explicitly documented and tested.
- Broker fault scenarios are covered in the shared suite (`supportsBrokerFault=true`).

### Database (`DriverDatabase`: MySQL/Postgres/SQLite)

- Acceptance is a committed queue-row insert. Claimed rows use stale-processing recovery, so application handlers must tolerate redelivery if finalization does not commit.
- Positive process and workflow facts wait for fenced row finalization matching the exact processing claim. Stale recovery invalidates the prior claim before another worker can reclaim the row; exhausted DELETE/UPDATE retries emit `settlement_failed`, retain the current row generation, and do not emit `process_succeeded`.
- Startup migrations remain enabled by default. Set `DisableAutoMigrate: true` when an external deployment process owns schema changes; the queue then performs no startup DDL. A migration failure does not permanently consume startup, so a later `Start` can retry after the underlying lock or permission problem is corrected.
- Processing fencing adds one nullable `processing_token` column, so existing rows and producer-only old binaries remain readable. When migrations are externally managed, add that column before starting new workers. Do not overlap old and new SQL workers during rollout: old workers settle by row ID and cannot honor the new claim-generation fence.
- The additive opt-out does not yet replace a versioned rollout policy. Operators that keep automatic migration enabled must grant the required DDL permissions and coordinate concurrent startup, especially for the uniqueness expiry index; MySQL and PostgreSQL concurrency/permission evidence remains open.
- Supports retry/backoff, poison retry, dispatch context cancellation, and deterministic duplicate prevention in the shared suite.
- Broker fault injection scenarios are not enabled in the shared suite for DB backends.
- DB backends rely on stale-`processing` recovery behavior for crash recovery (`process_recovered` event visibility is important operationally).
- Ordering is not currently claimed in the shared suite for MySQL/Postgres. SQLite ordering is only claimed in the suite under test-local conditions (see matrix note).

### NATS (`DriverNATS`)

- The current implementation uses Core NATS publish/subscribe, not JetStream. It is an ephemeral broker adapter: there is no durable consumer acknowledgement, retained work queue, or crash recovery boundary.
- Plain subscriptions are broadcast semantics, not competing-consumer queue semantics: every worker subscription on the same subject, whether in one process or several, can receive a copy.
- Worker startup reports success only after the server has observed the subscription, and failed startup can be retried. Shutdown waits for callbacks and delayed work already admitted to that worker before closing its producer connection.
- Initial and replacement publishes flush through a server roundtrip before reporting success. This proves only that the Core NATS server observed the ephemeral publish; it is not a durable queue acknowledgement.
- Retry is republish-based. Shutdown drains the worker's subscription before admitted handlers finish replacement publication; without another subscriber, Core NATS can accept and then discard that replacement. A process or connection failure can likewise lose the original or replacement message, so this backend does not currently conform to a durable committed-retry contract.
- The shared suite exercises core dispatch/processing and backoff/poison behavior only while the fixture remains available.
- Shared suite does not claim restart durability guarantees for delayed/retried work (`supportsRestart=false`, `supportsRestartDelayedDurability=false`).
- Deterministic no-duplicate and ordering guarantees are not claimed in the suite.
- Users should treat ordering as non-guaranteed unless a stronger constrained contract is explicitly added and tested.

### SQS (`DriverSQS`)

- Initial and replacement acceptance require a successful SQS `SendMessage` response with a non-empty service-generated message ID. SQS delivery is redeliverable through visibility timeout, so handlers must be idempotent.
- Retry republishes before deleting the original. Positive process and workflow facts wait for `DeleteMessage`; missing receipts or delete failures emit `settlement_failed` and leave the original eligible for redelivery, so duplicate handling remains mandatory.
- Supports restart recovery in the shared suite, but not delayed/retry restart durability guarantees (`supportsRestartDelayedDurability=false`).
- Broker fault scenarios are not deterministically exercised in the shared suite.
- Ordering guarantees are not claimed.
- Workers do not yet extend message visibility while a handler runs, and each receive can return several messages that one worker processes sequentially. Configure visibility for the worst-case interval from receive through completion of the last message in that batch, and expect duplicate delivery if processing exceeds it; M4-03 tracks heartbeat/extension support.
- Local integration validation uses LocalStack.

### RabbitMQ (`DriverRabbitMQ`)

- Initial and replacement persistent publishes require a positive publisher confirmation before dispatch succeeds or the original delivery is acknowledged.
- Worker retry publishes before acknowledging the original. Positive process and workflow facts wait for Ack. A negative confirmation permits safe claim compensation; a missing, canceled, or failed confirmation is treated as ambiguous and does not trigger reconnect-republish or uniqueness release. Ack/Nack failures emit `settlement_failed` because the original may redeliver.
- Supports restart recovery and delayed/retry restart durability in the shared suite.
- A worker does not yet reconnect after its delivery channel closes; reconstruct the queue runtime after connection loss. Dial retries and AMQP channel/connection closure are not fully context-aware and can overrun a caller's lifecycle deadline.
- Broker fault scenarios are not deterministically exercised in the shared suite.
- Ordering guarantees are not claimed.

## Ordering Guarantee Rules (Current Public Position)

Until the shared ordering contract is split into condition-specific scenarios, the safe public posture is:

- Ordering is **not guaranteed by default** across backends.
- Any FIFO behavior observed under a specific backend/test setup should be treated as a constrained implementation detail unless documented here with explicit preconditions.
- Retries, delays, and multi-worker concurrency can reorder execution and should be assumed to do so unless a backend-specific guarantee says otherwise.

## Production Guidance Notes

- Treat this matrix as the contract for what the shared integration suite validates today.
- Do not infer cross-process `UniqueFor` behavior from an instance-scoped backend. Use the scope and failure boundaries documented above.
- If you change a capability flag in `integration/all/integration_scenarios_test.go`, update this document in the same PR.
- If you want to claim a stronger backend guarantee publicly, add or unskip the corresponding shared scenario first.

## GA Completion Criteria for This Document

Before GA, ensure this document also includes:

- backend-specific configuration guidance links (timeouts, concurrency, retries)
- any known caveats/limits by backend version
- links to the latest passing shared integration evidence and soak evidence
