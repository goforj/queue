# Workflow Architecture (Historical Bus Design)

> **Status:** This is the original bus design record, retained to explain the
> version-one wire and API constraints. It is not the current architecture or
> implementation roadmap; use `plan.md` for both.

## Current Ownership

- `*queue.Queue` is the sole canonical application facade for dispatch, handlers, middleware, chains, batches, workflow state, stores, and observation.
- `internal/workflow.Engine` owns orchestration implementation. It depends on the neutral `busruntime.Runtime` transport seam and does not import root `queue`, public `bus`, or `queuecore`.
- Public messages, results, middleware, persisted workflow records, and store contracts are physical root `queue` types. Private adapters translate them at the engine boundary so GoDoc, reflection, generators, and custom stores never expose `internal/workflow` as their apparent owner.
- Public `bus` is a deprecated compatibility package. `bus.New(existingQueue)` returns an option-free adapter over that queue's existing engine; it does not register a second engine. Construction options and `NewWithStore` are rejected for an already-built queue and must instead be supplied through root queue options.
- The legacy raw-`busruntime.Runtime` construction route remains temporarily supported for integrations and preserves its observer, store, clock, and middleware options.
- `bus.Job` remains a boundary DTO because its public fields, composite literals, deferred JSON encoding, and raw string/byte semantics cannot alias `queue.Job` compatibly. `bus.JobOptions` is a source-compatible alias of the root persisted-options shape, and the facade converts the job once into the canonical root path.
- The self-returning `bus.ChainBuilder` and `bus.BatchBuilder` interfaces remain physical deprecated contracts. Keeping them distinct avoids breaking downstream type switches and custom implementations; their adapters still delegate every operation to the canonical engine.
- The legacy `bus.Event` observer shape remains only at that compatibility boundary. Root `queue.Observer` is the canonical event model and the internal engine has one event producer.
- Version-one physical names (`bus:job`, `bus:chain:node`, `bus:batch:job`, `bus:callback`), JSON envelopes, and SQL tables remain stable compatibility contracts despite the historical prefix.

## Compatibility Migration

Ordinary source forms remain supported: custom `bus.Bus`, store, middleware, observer, and builder implementations; keyed and unkeyed legacy DTO literals; the Temporal adapter; and the legacy fake all compile against the facade. The following runtime/tooling identity migration is intentional:

- compatible `bus` message, result, middleware, workflow-record, and store aliases now have the root package identity `github.com/goforj/queue`;
- `bus.Job`, `bus.Event`, `bus.Observer`, `bus.Bus`, `bus.Option`, `bus.ChainBuilder`, and `bus.BatchBuilder` retain their legacy `github.com/goforj/queue/bus` identity;
- code that keys behavior on `%T`, `reflect.Type.PkgPath`, gob/interface registration names, generated registries, dependency-injection keys, or a custom type-sensitive persistence format must map the applicable old `bus` names to the root `queue` names.

One configuration and runtime-behavior incompatibility is intentional: every option-free `bus.New(existingQueue)` facade now shares the root queue's handler registry, store, observer, middleware, and lifecycle instead of constructing independent state over the same physical runtime. Code that deliberately relied on isolated root and bus state must use distinct queue runtimes; ordinary callers should register and configure one root queue and treat `bus` only as a compatibility view. `bus.New(existingQueue, nonNilOption...)` and `bus.NewWithStore(existingQueue, ...)` now return `bus.ErrQueueOptionsUnsupported` because those options cannot configure only the shared view. Supply `queue.WithObserver`, `queue.WithStore`, `queue.WithClock`, and `queue.WithMiddleware` when constructing the root queue, then call `bus.New(existingQueue)` without options. The retained raw-`busruntime.Runtime` route continues to accept legacy bus options.

The type-identity migration does not otherwise change physical delivery names, JSON payload encoding, SQL table layouts or rows, retry/workflow outcomes, operational rollout, or the minimum Go version. Literal wire and legacy-SQL fixtures guard those contracts.

The remainder of this document describes the superseded proposal. Examples that
construct or configure `bus` directly should not be treated as current guidance.

This document defines a `bus` package for GoForj that composes on top of `github.com/goforj/queue` and provides workflow orchestration primitives: dispatch, chain, batch, callbacks, middleware, events, and test fakes.

`bus` is not a pub/sub transport. Cross-service event streaming is out of scope for this package.

## Product Goal

Build orchestration as an additive layer:

- Keep direct queue usage unchanged for simple jobs:
  - `q.Register(...)`
  - `q.StartWorkers(ctx)`
  - `q.Dispatch(...)`
- Add a `bus` facade for workflow orchestration:
  - chain jobs in order
  - batch jobs in parallel
  - callbacks (`Then`, `Catch`, `Finally`, `Progress`)
  - bus-level lifecycle events/observer
  - Laravel-style testing fakes/assertions

## Hard Constraints

- Bus must use existing `queue.Queue` as execution substrate.
- Bus must not expose broker subscribe APIs.
- Bus internals must be deterministic and idempotent under retries.
- Bus remains backend-portable (no backend-specific orchestration logic in core).

## Package Shape

Proposed package (in this repository): `github.com/goforj/queue/bus`

Files:

- `bus.go`: constructors + facade
- `job.go`: job model + envelope mapping
- `registry.go`: handler registry
- `chain.go`: chain builder + progression
- `batch.go`: batch builder + lifecycle
- `middleware.go`: middleware contracts + pipeline
- `events.go`: bus event model + observer API
- `store.go`: store interfaces + records
- `store_memory.go`: in-memory store
- `store_sql.go`: SQL store
- `fake.go`: fake bus + assertions

## Public API (Proposed)

```go
package bus

type Bus interface {
	Register(jobType string, handler Handler)

	Dispatch(ctx context.Context, job Job) (DispatchResult, error)
	Chain(jobs ...Job) ChainBuilder
	Batch(jobs ...Job) BatchBuilder

	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error

	FindBatch(ctx context.Context, batchID string) (BatchState, error)
	FindChain(ctx context.Context, chainID string) (ChainState, error)
	Prune(ctx context.Context, before time.Time) error
}

type Handler func(ctx context.Context, j Context) error

type Job struct {
	Type    string
	Payload any
	Options JobOptions
}

type JobOptions struct {
	Queue     string
	Delay     time.Duration
	Timeout   time.Duration
	Retry     int
	Backoff   time.Duration
	UniqueFor time.Duration
}
```

Constructors:

```go
func New(q queue.Queue, opts ...Option) (Bus, error)
func NewWithStore(q queue.Queue, store Store, opts ...Option) (Bus, error)
func NewFake() *Fake
```

Options:

- `WithObserver(observer Observer)`
- `WithStore(store Store)`
- `WithClock(func() time.Time)`
- `WithMiddleware(middlewares ...Middleware)`

## Ergonomic Examples

### Single dispatch

```go
b, _ := bus.New(q)
b.Register("monitor:poll", handleMonitorPoll)
_, _ = b.Dispatch(ctx,
	bus.NewJob("monitor:poll", EndpointPayload{URL: "https://goforj.dev/health"}).
		OnQueue("monitor-critical").
		Retry(3).
		Backoff(500*time.Millisecond),
)
```

### Chain

```go
chainID, _ := b.Chain(
	bus.NewJob("monitor:poll", target),
	bus.NewJob("monitor:downsample", target),
	bus.NewJob("monitor:alert", target),
).OnQueue("monitor-critical").
	Catch(func(ctx context.Context, st ChainState, err error) error { return nil }).
	Finally(func(ctx context.Context, st ChainState) error { return nil }).
	Dispatch(ctx)
_ = chainID
```

### Batch

```go
batchID, _ := b.Batch(jobs...).
	Name("Monitor Sweep").
	OnQueue("monitor-scan").
	AllowFailures().
	Progress(func(ctx context.Context, st BatchState) error { return nil }).
	Then(func(ctx context.Context, st BatchState) error { return nil }).
	Catch(func(ctx context.Context, st BatchState, err error) error { return nil }).
	Finally(func(ctx context.Context, st BatchState) error { return nil }).
	Dispatch(ctx)
_ = batchID
```

### Middleware

```go
b, _ := bus.New(
	q,
	bus.WithMiddleware(
		bus.SkipWhen{
			Predicate: func(ctx context.Context, jc bus.Context) bool {
				return jc.JobType == "monitor:downsample"
			},
		},
		bus.FailOnError{},
	),
)
```

## Queue Integration Contract

Bus registers reserved internal job types on the underlying queue:

- `bus:job`
- `bus:chain:node`
- `bus:batch:job`
- `bus:callback`

Envelope includes `schema_version` (starting at `1`).

Execution flow:

1. User dispatches job/chain/batch via Bus.
2. Bus stores orchestration metadata (when required).
3. Bus enqueues internal envelope job(s) via `q.WithContext(ctx).Dispatch(...)`.
4. Queue workers execute bus internal handlers.
5. Bus executes registered user handler and updates orchestration state.
6. Bus enqueues next node(s)/callback jobs as needed.

## Event Model (In-Process)

Bus emits internal lifecycle events:

- Dispatch: `DispatchStarted`, `DispatchSucceeded`, `DispatchFailed`
- Job: `JobStarted`, `JobSucceeded`, `JobFailed`
- Chain: `ChainStarted`, `ChainAdvanced`, `ChainCompleted`, `ChainFailed`
- Batch: `BatchStarted`, `BatchProgressed`, `BatchCompleted`, `BatchFailed`, `BatchCancelled`
- Callback: `CallbackStarted`, `CallbackSucceeded`, `CallbackFailed`

Observer API:

```go
type Observer interface { Observe(Event) }
type ObserverFunc func(Event)
func MultiObserver(observers ...Observer) Observer
```

Event fields (minimum):

- `schema_version`
- `event_id`
- IDs: `job_id`, `chain_id`, `batch_id`, `attempt`
- `job_type`, `queue`
- `occurred_at`, `duration`
- `error` (optional)

## State Model and Store

```go
type Store interface {
	CreateChain(ctx context.Context, rec ChainRecord) error
	AdvanceChain(ctx context.Context, chainID string, completedNode string) (next *ChainNode, done bool, err error)
	FailChain(ctx context.Context, chainID string, cause error) error
	GetChain(ctx context.Context, chainID string) (ChainState, error)

	CreateBatch(ctx context.Context, rec BatchRecord) error
	MarkBatchJobStarted(ctx context.Context, batchID, jobID string) error
	MarkBatchJobSucceeded(ctx context.Context, batchID, jobID string) (BatchState, done bool, err error)
	MarkBatchJobFailed(ctx context.Context, batchID, jobID string, cause error) (BatchState, done bool, err error)
	CancelBatch(ctx context.Context, batchID string) error
	GetBatch(ctx context.Context, batchID string) (BatchState, error)
}
```

Implementations:

- `MemoryStore` (local/test default)
- `SQLStore` (recommended production)

## Failure, Idempotency, Retry Ownership

Chain:

- strict order
- fail-fast on first node failure
- `Catch` once
- `Finally` once

Batch:

- jobs execute independently
- if `AllowFailures=false`, first failure cancels batch
- if `AllowFailures=true`, remaining jobs continue
- `Catch` once on first failure
- `Finally` once on terminal state

Idempotency keys:

- `dispatch:<bus_id>`
- `chain_advance:<chain_id>:<node_id>`
- `callback:<workflow_id>:<callback_kind>`

Retry ownership:

- Queue owns transport retry timing.
- Bus owns orchestration state transitions.
- Bus does not run independent retry loops by default.

Callback failure policy:

- Commit terminal workflow state first.
- Execute callback as `bus:callback` job.
- Callback failure emits event and retries with capped attempts.
- Callback failure does not roll back terminal workflow state.

Retention:

- Default 7-day retention for completed/cancelled/failed orchestration records.
- Per-workflow override supported.
- Prune API required (`SQLStore` command in phase 2+).

## Testing Surface

`bus.Fake` assertions:

- `AssertNothingDispatched(t)`
- `AssertDispatched(t, jobType)`
- `AssertDispatchedOn(t, queue, jobType)`
- `AssertDispatchedTimes(t, jobType, n)`
- `AssertNotDispatched(t, jobType)`
- `AssertCount(t, n)`
- `AssertChained(t, expected []string)`
- `AssertBatched(t, predicate func(BatchSpec) bool)`
- `AssertBatchCount(t, n)`
- `AssertNothingBatched(t)`

## Bus Driver Strategy

`bus` runtime backends (initial proposal):

| Runtime | Role | Phase |
|:--|:--|:--|
| `queue` (existing queue drivers) | Default execution runtime for bus envelopes | 1 |
| `temporal` | Optional orchestration runtime adapter | 3 |

Notes:

- Phase 1 and 2 should run entirely on existing queue runtime.
- Temporal adapter is optional and should be a separate package (`bus/driver/temporal`).

## Rollout

### Phase 1

- Bus facade + registry
- single dispatch + chain
- in-process observer events
- fake assertions
- memory store

### Phase 2

- batch + callbacks
- SQL store + pruning
- find APIs

### Phase 3

- middleware library
- temporal runtime adapter
- richer test helpers

## Finalized Decisions

1. Use jobs `monitor:poll`, `monitor:downsample`, `monitor:alert` in examples.
2. Include `schema_version` in bus envelopes/events.
3. `MemoryStore` for local/tests, `SQLStore` for production.
4. Keep bus worker lifecycle queue-owned (`StartWorkers` remains queue-owned).
5. Keep bus scoped to orchestration only in this repository.
