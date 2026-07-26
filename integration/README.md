# Integration Module

This module contains integration and benchmark suites that were moved out of the root module to keep root test dependencies and imports lean.

## Layout

- `integration/all`
  - Shared backend matrix scenarios (`TestIntegrationScenarios_AllBackends`)
  - `Queue`-surface integration matrix (`TestIntegrationQueue_AllBackends`)
- `integration/root`
  - Root-package integration/contract/observability suites
  - Canonical root workflow success and failure callbacks across backends
  - SQL workflow-store concurrency coverage
  - Driver constructor/linking integration coverage
  - Integration benchmarks

Optional drivers are linked once per integration package via `drivers_link_test.go`
files (`integration/all`, `integration/root`) so individual test files
do not need repeated driver import blocks.

## Common Commands

Full integration module (requires Docker/testcontainers for external backends):

```bash
GOCACHE=/tmp/queue-gocache go test -tags=integration ./integration/... -count=1
```

Shared scenario matrix only:

```bash
GOCACHE=/tmp/queue-gocache go test -tags=integration ./integration/all -run '^TestIntegrationScenarios_AllBackends$' -count=1 -v
```

Canonical root workflow matrix only:

```bash
GOCACHE=/tmp/queue-gocache go test -tags=integration ./integration/root -run '^TestCanonicalWorkflowContract_AllExecutableBackends$' -count=1 -v
```

Local-only compile/smoke checks (no Docker):

```bash
INTEGRATION_BACKEND=null,sync,workerpool GOCACHE=/tmp/queue-gocache go test -tags=integration ./integration/... -run '^$'
```

Coverage uses the repository collector so root and driver code exercised by this
module is included. CI runs these backend profiles in parallel; `all` is the slow
local equivalent and may start every external service container:

```bash
scripts/coverage-codecov.sh unit
INTEGRATION_BACKEND=all scripts/coverage-codecov.sh integration
```

## Environment

- `INTEGRATION_BACKEND=<list>` filters enabled backends (comma-separated).
  - Default (when unset, or `all`): all backends
  - Use a subset to avoid Docker/testcontainers (for example `null,sync,workerpool,sqlite`)
- `RUN_SOAK=1` enables soak-tagged integration scenarios.
- Docker/testcontainers access is required for external backends (`redis`, `mysql`, `postgres`, `nats`, `sqs`, `rabbitmq`).
