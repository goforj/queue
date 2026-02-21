# Bus Implementation Checklist

Use this as the source of truth for issue creation and implementation order.

## Scope

- Build full `bus` design in this repository.
- Keep `bus` orchestration-only.
- Do not add pub/sub transport concerns into `bus`.

## Work Items

### Core API and Skeleton

- [x] Add `bus` package with facade and constructors.
- [x] Define `Bus`, `Job`, `JobOptions`, `Handler`, `Context`, `Observer`.
- [x] Add `WithObserver`, `WithStore`, `WithClock` options.
- [x] Add internal envelope model with `schema_version=1`.

Acceptance:

- `go test ./...` passes.
- Public API compiles from examples in `docs/bus-design.md`.

### Queue Runtime Integration

- [x] Register internal job types:
  - `bus:job`
  - `bus:chain:node`
  - `bus:batch:job`
  - `bus:callback`
- [x] Route all execution through underlying `queue.Queue`.
- [x] Keep worker lifecycle queue-owned (`StartWorkers`, `Shutdown` pass-through).

Acceptance:

- Queue-backed unit/integration tests verify envelopes execute through queue handlers.

### Handler Registry and Dispatch

- [x] Implement `Register(jobType, handler)`.
- [x] Implement `Dispatch(ctx, job)` end-to-end.
- [x] Emit dispatch and job lifecycle events.

Acceptance:

- Dispatch success/failure event assertions pass.
- Unknown job type fails deterministically with clear error.

### Chain Orchestration

- [x] Implement `Chain(...).Dispatch(ctx)`.
- [x] Execute chain nodes strictly in order.
- [x] Implement `Catch` (once) and `Finally` (once).
- [x] Stop chain on first failure.

Acceptance:

- Ordered execution test.
- Fail-fast test.
- `Catch` and `Finally` once-only tests.
- Duplicate node completion does not advance chain twice.

### Batch Orchestration

- [x] Implement `Batch(...).Dispatch(ctx)`.
- [x] Track `total`, `pending`, `processed`, `failed`.
- [x] Implement `AllowFailures` behavior.
- [x] Implement `Progress`, `Then`, `Catch`, `Finally`.

Acceptance:

- Progress math tests.
- First-failure cancellation test when `AllowFailures=false`.
- Continuation test when `AllowFailures=true`.
- Callback once-only tests under retries.

### Store Implementations

- [x] Implement `MemoryStore`.
- [x] Implement `SQLStore`.
- [x] Add `FindChain` and `FindBatch`.
- [x] Add prune API and SQL prune command path.

Acceptance:

- Store contract tests run against both stores.
- Restart/retry safety tests pass with SQL store.

### Idempotency and Retry Semantics

- [x] Enforce idempotency keys:
  - `dispatch:<bus_id>`
  - `chain_advance:<chain_id>:<node_id>`
  - `callback:<workflow_id>:<callback_kind>`
- [x] Keep queue as retry owner for transport timing.
- [x] Ensure callback failure does not roll back terminal workflow state.

Acceptance:

- Duplicate dispatch test.
- Duplicate chain-advance test.
- Duplicate callback job test.
- Retry behavior tests prove no duplicated orchestration transitions.

### Middleware

- [x] Add middleware pipeline contracts.
- [x] Implement deterministic middleware order.
- [x] Add built-ins:
  - `RetryPolicy`
  - `FailOnError`
  - `SkipWhen`
  - `WithoutOverlapping`
  - `RateLimit`

Acceptance:

- Middleware order test.
- Middleware short-circuit test.
- Error propagation and retry interaction tests.

### Testing Helpers

- [x] Implement `bus.Fake`.
- [x] Add assertions:
  - `AssertNothingDispatched`
  - `AssertDispatched`
  - `AssertDispatchedOn`
  - `AssertDispatchedTimes`
  - `AssertNotDispatched`
  - `AssertCount`
  - `AssertChained`
  - `AssertBatched`
  - `AssertBatchCount`
  - `AssertNothingBatched`

Acceptance:

- Fake contract tests cover each assertion method.

### Temporal Adapter (Optional Runtime)

- [x] Create `bus/driver/temporal` as separate adapter package.
- [x] Map bus primitives to Temporal workflows/activities.
- [x] Keep Temporal types out of core `bus` API.

Acceptance:

- Adapter compiles behind feature flag/build target.
- Adapter integration tests cover chain, batch, callback behavior.

## Global Test Gates

- [x] `go test ./...`
- [x] `go test -race ./...`
- [ ] `go test -tags integration ./...`
- [x] Duplicate-delivery/idempotency scenarios included in integration suite.

## Merge Criteria

- [ ] Bus README/examples added.
- [ ] API docs updated.
- [ ] No queue API regressions.
- [ ] CI green across unit/race/integration.
