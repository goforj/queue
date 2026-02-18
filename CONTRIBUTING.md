# Contributing

## Adding a new driver

When adding a new `Driver`, update test coverage in the same change. A new driver is not complete until all items below are done.

Queue contract mapping
- Update `TestOptionContractCoverage_AllDriversAccountedFor` in `contract_test.go`.
- Ensure `runQueueContractSuite(...)` runs for the driver in local and/or integration suites.

Worker contract mapping
- Update `TestWorkerContractCoverage_AllDriversAccountedFor` in `worker_contract_test.go`.
- Ensure `runWorkerContractSuite(...)` runs for the driver in local and/or integration suites.

Interoperability coverage
- Add or update tests that enqueue via `Queue` and consume via `Worker` using separate runtimes for the driver.
- Durable drivers must validate this explicitly.

Integration backend wiring
- If the driver uses external infrastructure, wire it into integration tests and backend selection.
- Keep integration runs executable with:
  - `RUN_INTEGRATION=1 go test -tags integration ./...`

CI matrix
- Ensure the integration backend matrix in `.github/workflows/test.yml` includes the backend.
- The `integration-matrix-guard` job enforces required backend entries.
