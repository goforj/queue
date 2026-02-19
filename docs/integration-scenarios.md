# Integration Scenarios Roadmap

This document tracks queue-runtime reliability integration scenarios across all enabled backends.

## Current baseline

Implemented in `integration_scenarios_test.go` via `TestIntegrationScenarios_AllBackends` for enabled backends (`redis`, `mysql`, `postgres`, `sqlite`, `nats`, `sqs`, `rabbitmq`).

Named scenarios currently enforced:

- `scenario_register_handler`
- `scenario_startworkers_idempotent`
- `scenario_dispatch_burst`
- `scenario_wait_all_processed`
- `scenario_poison_message_max_retry`
- `scenario_worker_restart_recovery`
- `scenario_bind_invalid_json`
- `scenario_unique_queue_scope`
- `scenario_dispatch_context_cancellation`
- `scenario_shutdown_during_delay_retry`
- `scenario_multi_worker_contention`
- `scenario_duplicate_delivery_idempotency`
- `scenario_dispatch_during_broker_fault`
- `scenario_consume_after_broker_recovery`
- `scenario_ordering_contract`
- `scenario_backpressure_saturation`
- `scenario_payload_large`
- `scenario_config_option_fuzz`
- `scenario_shutdown_idempotent`

Optional long-run scenario (enabled with `RUN_SOAK=1`):
- `scenario_soak_mixed_load`

## Latest trust snapshot

Last full run (local, Docker/testcontainers):
- Date: February 19, 2026
- Command: `RUN_INTEGRATION=1 GOCACHE=/tmp/queue-gocache go test -tags integration ./... -count=1`
- Result: **pass**

Scenario suite status (`TestIntegrationScenarios_AllBackends`):

| Backend | Status | Notes |
|:--|:--:|:--|
| Redis | pass | Baseline scenarios passed (soak intentionally skipped without `RUN_SOAK=1`). |
| MySQL | pass | Baseline scenarios passed; ordering scenario capability-gated (skipped). |
| Postgres | pass | Baseline scenarios passed; ordering scenario capability-gated (skipped). |
| SQLite | pass | Baseline scenarios passed; capability-gated scenarios skipped as expected. |
| NATS | pass | Baseline scenarios passed with capability-based skips (ordering/restart/fault injection). |
| SQS | pass | Baseline scenarios passed with expected capability-gated skips (ordering/restart/fault injection). |
| RabbitMQ | pass | Baseline scenarios passed with expected capability-gated skips (ordering/fault injection). |

Observability suite status (`TestObservabilityIntegration_AllBackends`):

- Passed across all enabled backends in current run.
- `TestObservabilityIntegration_PauseResumeSupport_AllBackends` also passed for supported backends.

What this proves today:

- Worker lifecycle idempotency (`StartWorkers` and `Shutdown` are safe when called twice).
- Concurrent dispatch pressure with mixed task options (`Delay`, `Timeout`, `Retry`, `Backoff` where supported).
- Payload decode path with `Task.Bind`.
- Poison-message behavior is enforced per-backend capability in the fixture matrix (retry ceiling where supported, plus healthy-task recovery after poison).
- Worker restart recovery is validated only on backends marked restart-capable in the fixture matrix.
- Invalid JSON payload decode behavior via `Task.Bind` is exercised, followed by successful processing of a valid payload.
- Queue-scoped uniqueness is validated (same queue duplicate rejected, different queue accepted).
- Dispatch context-cancellation behavior is validated per backend capability, with healthy follow-up task processing.
- Shutdown during delayed and retry workloads is validated with restart/recovery checks on supported backends.
- Multi-worker contention is validated for deterministic backends to ensure single successful processing per task.
- Duplicate-delivery idempotency patterns are validated under forced retry with single side-effect commit.
- Broker fault injection and consume-after-recovery flow is validated on supported backends.
- FIFO ordering contract is validated for backends marked ordering-capable.
- Backpressure saturation preserves forward progress and probe processing.
- Large payload handling is validated end-to-end.
- Config/task-option fuzz coverage validates mixed option combinations across backends for stability.
- Long-run soak mixed-load behavior is available via opt-in (`RUN_SOAK=1`).
- RabbitMQ delayed/retry durability now uses broker-side delay queues (TTL + dead-letter) so delayed work survives worker restarts.
- Redis dispatch now applies a default task timeout when none is provided, preventing timeout/deadline-missing runtime edge cases.
- End-to-end completion for successfully dispatched tasks.

## Next scenarios

High-concurrency soak (long-running)
- Candidate scenario: `scenario_soak_mixed_load` extensions.
- Goal: extend with longer durations and tighter throughput/latency assertions.
- Status: planned (tag-gated).

Large and edge payload behavior
- Candidate scenario: `scenario_payload_bind_invalid_json`.
- Goal: verify near-limit payloads, empty payloads, malformed payload decode handling.
- Status: planned.

Backpressure and queue saturation
- Candidate scenario: `scenario_backpressure_saturation` extensions.
- Goal: extend with stricter queue-depth and latency threshold assertions.
- Status: planned.

Context cancellation and shutdown races
- Candidate scenario: `scenario_shutdown_during_delay_retry` extensions.
- Goal: add stricter timing and race invariants under heavy mixed traffic.
- Status: planned.

Multi-worker contention
- Candidate scenario: `scenario_multi_worker_contention` extensions.
- Goal: extend with larger fan-out and churn (workers joining/leaving mid-run).
- Status: planned.

Config/task-option fuzzing
- Candidate scenario: `scenario_config_option_fuzz` extensions.
- Goal: extend with invalid-combination mutation buckets and error-shape assertions.
- Status: planned.

Observability contract
- Candidate scenario: `scenario_observability_contract`.
- Goal: once hooks exist, assert emitted counters/events for dispatch, success, retry, fail, duplicate.
- Status: complete (implemented in `observability_integration_test.go` via `TestObservabilityIntegration_AllBackends` and `TestObservabilityIntegration_PauseResumeSupport_AllBackends`).

## Execution model

- `smoke`: always-on integration scenarios (current baseline).
- `chaos`: scheduled fault-injection scenarios (`scenario_dispatch_during_broker_fault`, `scenario_consume_after_broker_recovery`, and race-heavy scenarios).
- `soak`: extended runtime scenarios; isolated from normal CI when needed.
