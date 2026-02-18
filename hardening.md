# Hardening Roadmap

This document tracks queue-runtime reliability hardening across all enabled integration backends.

## Current baseline

Implemented in `integration_backends_integration_test.go` via `TestIntegrationHardening_AllBackends` for enabled backends (`redis`, `mysql`, `postgres`, `sqlite`, `nats`, `sqs`, `rabbitmq`).

Named steps currently enforced:

- `step_register_handler`
- `step_start_idempotent`
- `step_enqueue_burst`
- `step_wait_all_processed`
- `step_poison_message_max_retry`
- `step_worker_restart_recovery`
- `step_bind_invalid_json`
- `step_unique_queue_scope`
- `step_enqueue_context_cancellation`
- `step_shutdown_during_delay_retry`
- `step_multi_worker_contention`
- `step_duplicate_delivery_idempotency`
- `step_enqueue_during_broker_fault`
- `step_consume_after_broker_recovery`
- `step_ordering_contract`
- `step_backpressure_saturation`
- `step_payload_large`
- `step_config_option_fuzz`
- `step_shutdown_idempotent`

Optional long-run step (enabled with `RUN_SOAK=1`):
- `step_soak_mixed_load`

What this proves today:

- Worker lifecycle idempotency (`Start` and `Shutdown` are safe when called twice).
- Concurrent enqueue pressure with mixed task options (`Delay`, `Timeout`, `Retry`, `Backoff` where supported).
- Payload decode path with `Task.Bind`.
- Poison-message behavior is enforced per-backend capability in the fixture matrix (retry ceiling where supported, plus healthy-task recovery after poison).
- Worker restart recovery is validated only on backends marked restart-capable in the fixture matrix.
- Invalid JSON payload decode behavior via `Task.Bind` is exercised, followed by successful processing of a valid payload.
- Queue-scoped uniqueness is validated (same queue duplicate rejected, different queue accepted).
- Enqueue context-cancellation behavior is validated per backend capability, with healthy follow-up task processing.
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
- Redis enqueue now applies a default task timeout when none is provided, preventing timeout/deadline-missing runtime edge cases.
- End-to-end completion for successfully enqueued tasks.

## Next scenarios

High-concurrency soak (long-running)
- Candidate step: `step_soak_mixed_load` extensions.
- Goal: extend with longer durations and tighter throughput/latency assertions.
- Status: planned (tag-gated).

Large and edge payload behavior
- Candidate step: `step_payload_bind_invalid_json`.
- Goal: verify near-limit payloads, empty payloads, malformed payload decode handling.
- Status: planned.

Backpressure and queue saturation
- Candidate step: `step_backpressure_saturation` extensions.
- Goal: extend with stricter queue-depth and latency threshold assertions.
- Status: planned.

Context cancellation and shutdown races
- Candidate step: `step_shutdown_during_delay_retry` extensions.
- Goal: add stricter timing and race invariants under heavy mixed traffic.
- Status: planned.

Multi-worker contention
- Candidate step: `step_multi_worker_contention` extensions.
- Goal: extend with larger fan-out and churn (workers joining/leaving mid-run).
- Status: planned.

Config/task-option fuzzing
- Candidate step: `step_config_option_fuzz` extensions.
- Goal: extend with invalid-combination mutation buckets and error-shape assertions.
- Status: planned.

Observability contract
- Candidate step: `step_observability_contract`.
- Goal: once hooks exist, assert emitted counters/events for enqueue, success, retry, fail, duplicate.
- Status: complete (implemented in `observability_integration_test.go` via `TestObservabilityIntegration_AllBackends` and `TestObservabilityIntegration_PauseResumeSupport_AllBackends`).

## Execution model

- `smoke`: always-on integration hardening steps (current baseline).
- `chaos`: scheduled fault-injection scenarios (`step_enqueue_during_broker_fault`, `step_consume_after_broker_recovery`, and race-heavy steps).
- `soak`: extended runtime scenarios; isolated from normal CI when needed.
