# Contributing

## Adding a new driver

When adding a new `Driver`, update test coverage in the same change. A new driver is not complete until all items below are done.

Queue contract mapping
- Update `TestOptionContractCoverage_AllDriversAccountedFor` in `contract_test.go`.
- Ensure `runQueueContractSuite(...)` runs for the driver in local and/or integration suites.

Interoperability coverage
- Add or update tests that dispatch via `Queue` and consume via queue-managed workers for the driver.
- Durable drivers must validate this explicitly.

Integration backend wiring
- If the driver uses external infrastructure, wire it into integration tests and backend selection.
- Keep integration runs executable with:
  - `RUN_INTEGRATION=1 go test -tags integration ./...`

CI matrix
- Ensure the integration backend matrix in `.github/workflows/test.yml` includes the backend.
- The `integration-matrix-guard` job enforces required backend entries.
- Nightly/manual soak scenario runs are defined in `.github/workflows/soak.yml` with `RUN_SOAK=1`.

## Integration scenarios

The integration scenarios suite runs for every enabled backend in `integration_scenarios_test.go` under `TestIntegrationScenarios_AllBackends`.

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

What these prove:
- Worker lifecycle idempotency (`StartWorkers` and `Shutdown` can be called twice safely).
- Concurrent dispatch pressure with mixed job options (`Delay`, `Timeout`, `Retry`, `Backoff` where supported).
- Payload decode path via `Job.Bind(...)`.
- Poison job behavior by backend capability (retry ceiling where supported) and healthy-job recovery.
- Restart recovery only for backends that support deterministic restart durability in integration.
- Invalid JSON payload behavior through `Job.Bind(...)` does not wedge workers; valid payloads still process.
- Uniqueness is enforced per queue (duplicate rejected in same queue, allowed across queues).
- Dispatch context cancellation behavior is verified per backend capability, with healthy follow-up processing.
- Worker shutdown during delayed/retry workloads is exercised with restart and recovery assertions where supported.
- Multiple workers on one queue are validated against duplicate successful processing in deterministic backends.
- Duplicate-delivery idempotency patterns are exercised under forced retry to validate single side-effect commit.
- Broker fault injection/recovery is exercised on supported backends.
- FIFO ordering contract is asserted for backends marked ordering-capable.
- Backpressure saturation scenarios prove continued forward progress.
- Large payload processing is validated end-to-end.
- Config/job-option fuzz coverage validates mixed option combinations across backends for stability.
- End-to-end completion for all successfully dispatched jobs.

