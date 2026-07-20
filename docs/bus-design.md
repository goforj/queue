# Workflow Architecture (Historical Bus Design)

> **Status:** This is the original bus design record, retained to explain the
> version-one wire and API constraints. It is not the current architecture or
> implementation roadmap; use [`docs/plan.md`](./plan.md) for both.

## Current Ownership

- `*queue.Queue` is the sole canonical application facade for dispatch, handlers, middleware, chains, batches, workflow state, stores, and observation.
- `internal/workflow.Engine` owns orchestration implementation. It depends on the neutral `busruntime.Runtime` transport seam and does not import root `queue`, public `bus`, or `queuecore`.
- Public messages, results, middleware, persisted workflow records, and store contracts are physical root `queue` types. Private adapters translate them at the engine boundary so GoDoc, reflection, generators, and custom stores never expose `internal/workflow` as their apparent owner.
- Public `bus` is a deprecated compatibility package. `bus.New(existingQueue)` returns an option-free adapter over that queue's existing engine; it does not register a second engine. Construction options and `NewWithStore` are rejected for an already-built queue and must instead be supplied through root queue options.
- The legacy raw-`busruntime.Runtime` construction route remains temporarily supported for integrations and preserves its observer, store, clock, and middleware options.
- `bus.Job` remains a boundary DTO because its public fields, composite literals, deferred JSON encoding, and raw string/byte semantics cannot alias `queue.Job` compatibly. `bus.JobOptions` is a source-compatible alias of the root persisted-options shape, and the facade converts the job once into the canonical root path.
- The self-returning `bus.ChainBuilder` and `bus.BatchBuilder` interfaces remain physical deprecated contracts. Keeping them distinct avoids breaking downstream type switches and custom implementations; their adapters still delegate every operation to the canonical engine.
- The legacy `bus.Event` observer shape remains only at that compatibility boundary. Root `queue.Observer` is the canonical event model and the internal engine has one event producer.
- `queue.FakeQueue` owns the only fake state and runs chain/batch construction through the production workflow engine and memory-store contract. Deprecated `bus.Fake` and `queuefake.Fake` values are typed compatibility views over that same concurrency-safe recorder; they do not own independent dispatch, builder, or assertion models.
- Version-one physical names and JSON envelopes remain readable compatibility contracts despite the historical prefix. Root direct dispatch now uses the application job type and payload; `bus:job` remains registered for old backlog, reserved-name collisions, the migration option, and the raw-runtime compatibility route. Chain, batch, and callback deliveries retain `bus:chain:node`, `bus:batch:job`, and `bus:callback`.

## Compatibility Migration

Ordinary source forms remain supported: custom `bus.Bus`, store, middleware, observer, and builder implementations; keyed and unkeyed legacy DTO literals; the Temporal adapter; and the legacy fake all compile against the facade. The following runtime/tooling identity migration is intentional:

- compatible `bus` message, result, middleware, workflow-record, and store aliases now have the root package identity `github.com/goforj/queue`;
- `bus.Job`, `bus.Event`, `bus.Observer`, `bus.Bus`, `bus.Option`, `bus.ChainBuilder`, and `bus.BatchBuilder` retain their legacy `github.com/goforj/queue/bus` identity;
- code that keys behavior on `%T`, `reflect.Type.PkgPath`, gob/interface registration names, generated registries, dependency-injection keys, or a custom type-sensitive persistence format must map the applicable old `bus` names to the root `queue` names.

One configuration and runtime-behavior incompatibility is intentional: every option-free `bus.New(existingQueue)` facade now shares the root queue's handler registry, store, observer, middleware, and lifecycle instead of constructing independent state over the same physical runtime. Code that deliberately relied on isolated root and bus state must use distinct queue runtimes; ordinary callers should register and configure one root queue and treat `bus` only as a compatibility view. `bus.New(existingQueue, nonNilOption...)` and `bus.NewWithStore(existingQueue, ...)` now return `bus.ErrQueueOptionsUnsupported` because those options cannot configure only the shared view. Supply `queue.WithObserver`, `queue.WithStore`, `queue.WithClock`, and `queue.WithMiddleware` when constructing the root queue, then call `bus.New(existingQueue)` without options. The retained raw-`busruntime.Runtime` route continues to accept legacy bus options.

Fake runtime behavior is also intentionally corrected: abandoned builders no longer satisfy chain or batch assertions, invalid or canceled dispatches remain absent, builder options are retained, returned workflow IDs identify lookup state, effective default queues are assertion-visible, and Reset clears direct plus workflow records from every compatibility view. Recording fakes accept closure callbacks for fluent compatibility but do not retain them in fake runtime state or execute them. Tests that asserted the old constant `fake-chain` or `fake-batch` identifiers must instead treat returned IDs as opaque and may use `FindChain` or `FindBatch`; tests that deliberately depended on separate queue/workflow direct histories must migrate to the unified direct assertions. Constructor signatures, the zero value, value copies after initialization, and physical `bus.Fake`/`bus.BatchSpec` identities remain source-compatible; configuration, persisted data, wire formats, operations, and the minimum Go version are unchanged.

The type-identity migration itself did not change wire or persistence contracts. The later direct-delivery cutover deliberately does; see [Direct Delivery Migration](direct-delivery-migration.md) for the exact wire, SQL, runtime, and rollout boundary. Literal legacy-wire and legacy-SQL fixtures continue to guard backward reading.

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
func New(q any, opts ...Option) (Bus, error)
func NewWithStore(q any, store Store, opts ...Option) (Bus, error)
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
type Observer interface { Observe(context.Context, Event) }
type ObserverFunc func(context.Context, Event)
func MultiObserver(observers ...Observer) Observer
```

Event fields (minimum):

- `schema_version`
- `event_id`
- IDs: `job_id`, `chain_id`, `batch_id`, `attempt`
- `job_type`, `queue`
- `occurred_at`, `duration`
- `error` (optional)

The observer event schema and the workflow-envelope protocol are separate version domains even though both currently start at `1`. Envelope `schema_version` governs internal workflow dispatch decoding. `Event.SchemaVersion` governs the shared queue/worker/workflow observer contract, and transition-receipt `event_schema_version` pins only that observer contract.

## State Model and Store

```go
type WorkflowStore interface {
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

	MarkCallbackInvoked(ctx context.Context, key string) (bool, error)
	Prune(ctx context.Context, before time.Time) error
}
```

Stores that execute across competing workers can add the compatible outcome capability:

```go
type WorkflowOutcomeStore interface {
	WorkflowStore
	FailChainNode(ctx context.Context, chainID, nodeID string, cause error) (ChainState, bool, error)
	SettleBatchJob(ctx context.Context, batchID, jobID string, outcome BatchJobOutcome, cause error) (BatchState, bool, error)
}
```

Implementations:

- `MemoryStore` (local/test default)
- `SQLStore` (recommended production)

Workflow creation requires a non-empty workflow ID, at least one chain node or batch member, and a non-empty unique ID for every node or member. Builders already produce records with those properties; applications that call `WorkflowStore` directly must do the same. The built-in memory store snapshots chain nodes and payload bytes during creation and returns isolated copies, so mutating an input record, successor, or `ChainState` does not mutate persisted state.

Both implementations claim a chain node or batch member before changing its parent state. SQL performs that claim and an arithmetic parent update in one transaction, so duplicate delivery cannot advance twice and concurrent batch members cannot overwrite one another's counters. The same concurrency contract runs against SQLite, MySQL, and PostgreSQL.

Built-in stores also implement the additive `WorkflowOutcomeStore` capability. It gives successful and failed deliveries of one chain node or batch member a single first-writer settlement boundary. A contradictory late delivery is acknowledged without changing the committed outcome or aggregate counters; it emits no losing logical job/workflow fact, advances no application progress, and invokes no callback. Chain transitions compare the persisted node order and `NextIndex`; batch transitions report whether the requested outcome category owns the already-claimed member. The established batch schema and `BatchState` do not retain a per-member failure cause, so the `cause` argument remains delivery-local metadata rather than part of first-writer ownership. Persisted chain failures retain their first authoritative cause; built-in `FailChain` is now a no-op for an already-terminal chain instead of overwriting that cause. The base `WorkflowStore` remains source-compatible for established custom stores, but a custom implementation must add `WorkflowOutcomeStore` to provide the public atomic contradictory-category guarantee across processes.

The built-in engine store has a narrower private contract as well. Its `claimedNow` result means only that the current store call performed the transition; it is response-local and is not durable owner proof. For durable ownership, memory and SQL built-ins record an immutable transition receipt containing its receipt format version, reconstructed observer-event schema version, workflow incarnation, member outcome, physical delivery generation and attempt, and correlated job identity. SQL writes that receipt in the same transaction as the workflow mutation; memory retains it only for the life of the process. Both version fields are currently `1`. A runtime fails recovery closed when either `receipt_version` or `event_schema_version` is unsupported: it returns an uncommitted error and neither acknowledges the delivery, executes application code, marks application state committed, nor reconstructs facts from a format it does not understand. Logical receipt validation requires a complete valid persisted owner, including a nonnegative owner attempt; nonempty current dispatch/`JobID`; matching workflow kind/ID/member/incarnation and owner dispatch; and an immutable job fingerprint match. The current attempt is only physical provenance and may differ from the owner or be negative. Chain duplicates may also carry a different physical `JobID`; batch `JobID` remains its logical member key and must match. A logically valid receipt proves the application transition and suppresses handler replay. Reconstructing successful member or aggregate facts additionally requires exact recovered generation, current attempt, and physical `JobID` ownership. Queue provenance alone cannot prove either boundary.

After the current delivery both claims a built-in transition and obtains its receipt, the engine marks application state committed on that delivery's settlement boundary. This is a provenance handoff, not queue settlement: if later workflow infrastructure requires same-attempt redelivery, SQL retains the current generation as the receipt owner instead of continuing to carry an older recovered generation. A numbered application retry still clears the link. Focused settlement, SQL-token, and chain post-transition tests cover this handoff, while real SQLite, MySQL, and PostgreSQL tests now cover receipt-backed terminal-chain recovery after forced finalization failure.

A logically valid failed chain receipt uses the authoritative `ChainState.Failure` to return a permanent physical outcome across exact, different, or legacy recovered-generation provenance and across different attempts or physical `JobID`s. An empty persisted cause returns a permanent diagnostic rather than success. Recovery does not re-run the handler, Catch/Finally callbacks, or occurrence-based `JobFailed`/`ChainFailed` facts. Invalid receipt/event version, incomplete owner, logical dispatch/job-content mismatch, workflow incarnation, outcome, or aggregate flags return an uncommitted error before application code; physical nonownership alone does not. A receipt-absent legacy built-in row and application-defined/decorated stores retain the compatibility fallback, which may execute the handler once to preserve terminal physical classification while still suppressing duplicate facts and callbacks. Failure-receipt insertion and parent failure are one SQL transaction, and a receipt-insert fault rolls both back. A real SQLite queue fixture forces the initial archive plus multiple recovery archives to fail, verifies first-cause and generation lineage survive at attempt zero, and then reaches `dead` at attempt one with the persisted cause and only one application/workflow failure occurrence. Equivalent MySQL/PostgreSQL failed-chain finalization fixtures remain open.

For batches, the terminal member's transaction also records aggregate completion ownership. Built-in memory settlement holds one mutex; MySQL and PostgreSQL lock the parent row after the member compare-and-swap, so only the first false-to-true terminal transition can create the aggregate-owner receipt. Real twelve-member, twelve-worker fail-fast races on both server dialects prove one aggregate receipt and one failed/cancelled terminal fact pair while every member retains its own receipt. Because batch `JobID` is the logical member key, it must match for replay suppression. A valid duplicate with a different attempt, recovered generation, or legacy provenance still suppresses its handler; successful duplicates settle without facts, while failed duplicates return a generic permanent cause because the original application cause is not persisted. A SQL aggregate row is valid only as a completed transition; cancellation must own failure; and an aggregate row that names the requested member must match that member receipt's workflow incarnation, complete owner, and outcome. Recovery also checks those flags against live terminal state. Missing or contradictory proof fails uncommitted before handlers, callbacks, state-commit signaling, or facts. `BatchCompleted` may be reconstructed for a batch of any size only when this validated aggregate receipt names the exact recovered generation, current attempt, and `JobID`; completed aggregate state without that exact fact owner is never sufficient. `TestSQLStoreBatchAggregateOwnershipMismatchFailsClosed`, `TestSQLStoreBatchAggregateIncarnationMismatchFailsClosed`, and `TestBatchRecoveryRejectsInvalidAggregateReceiptShape` pin these corruption boundaries. `TestDatabaseIntegration_SQLite/sqlite_terminal_batch_completion_recovers_from_completing_member` proves the two-member recovery case without executing either handler again or attributing completion to the earlier member. The SQL delivery reaches its terminal `dead` archive on failed recovery rather than being acknowledged as success, and no fabricated failure or member fact is published. The normal `queue.WithStore(queue.NewSQLStore(...))` path unwraps the built-in store so this private contract remains available, and an option-free `bus.New(existingQueue)` shares that same engine. Application-defined stores, decorators around built-ins, and the retained raw-runtime bus route expose only public store capabilities. They remain source-compatible and can retain first-writer outcome-category semantics through `WorkflowOutcomeStore`, but they do not receive the exact built-in generation/receipt or `claimedNow` guarantees.

Any recovered predecessor whose validated durable state proves success checks live `NextIndex` and re-dispatches the immediate successor while it has not progressed, without re-running the predecessor. Exact recovered generation, attempt, and physical `JobID` ownership can also reconstruct the predecessor's deferred facts. A missing receipt, an application-defined/decorated store without receipt capability, or a logically valid receipt with different/legacy generation, different attempt, or different physical `JobID` restores only the live continuation; it emits no predecessor facts or callbacks. A supported success receipt is logically validated before this liveness fallback: it cannot own cancellation, and its completion flag must exactly match whether the predecessor is final. Corruption returns an uncommitted outcome with no dispatch or effects. A successor enqueue rejection is likewise uncommitted so recovery can try again. This repairs definite enqueue rejection and legacy/custom liveness, but it is deliberately at-least-once: the predecessor row cannot distinguish a missing successor from one already queued but not yet reflected in workflow state, so recovery may enqueue a duplicate. Once the successor has progressed or the chain is terminal, recovery does not dispatch it again.

Transition receipts are not observer or continuation outboxes. They neither prove callback delivery nor retain `Progress` closures, successor-enqueue acceptance, batch fan-out, or deferred observer invocation after queue settlement. After a recovered SQL delivery exhausts finalization retries, the driver makes a fenced best-effort repair that restores its inherited receipt lineage on the same attempt and returns it to `pending` with a bounded delay. Real SQLite success, failed-chain, and failed-batch scenarios force multiple recovery finalization failures before later settlement and prove no handler replay while the repair succeeds. The repair cannot cover a failed repair transaction, physical commit/readback ambiguity, or a row already removed by successful queue settlement. Durable continuation intents and a settlement outbox remain roadmap work.

MySQL key validation follows the capacities discovered from every identity column used by a workflow and its receipt. A wholly fresh auto-schema uses 255-byte workflow/member and receipt identities plus 512-byte callback keys. When only the receipt table is missing beside established state, ordinary startup derives its `workflow_id` width as the larger of the effective chain and batch ID capacities and its `member_id` width as the larger of the chain-node and batch-job capacities. A real upgrade fixture widens legacy state to 512 bytes, drops only the receipt table, and proves startup recreates it at 512/512 while accepting identities above the fresh defaults. Existing tables are never altered: a pre-existing receipt instead intersects the accepted capacities discovered from the complete live schema. An incompatible existing receipt therefore requires a quiescent managed migration and a new store instance. If the derived three-column primary key exceeds the server's indexed-key budget, creation fails with the derived widths and schema-first guidance; it does not silently narrow established identities. Operators must precreate a compatible indexed receipt schema or explicitly migrate supported identity limits and existing data before rollout.

Caller-managed workflow identity columns, including `bus_workflow_transition_receipts.workflow_id` and `.member_id`, must use `VARBINARY`; `VARCHAR`, `TEXT`, and fixed-width `BINARY` are rejected because they do not provide the same byte-exact round-trip contract. `queue.NewSQLStore` retains the legacy behavior of enabling schema creation even when compatibility field `SQLStoreConfig.AutoMigrate` is false; use `queue.NewSQLStoreWithManagedSchema` only after provisioning every required table and both receipt version columns. Before upgrading an incompatible schema, quiesce workflow writers, audit case- or padding-equivalent keys for collisions, convert all identity columns and align their receipt widths in one maintenance window, then restart workers with a new store instance. This is a MySQL persisted-schema, runtime-behavior, and operational rollout concern, not a source/API, configuration-file, wire, or minimum-Go-version change. Real MySQL and PostgreSQL tests exercise fresh auto-created receipt tables, serialized aggregate ownership, and receipt-backed recovery. On rollback, quiesce new workers and leave the receipt table in place for a later re-upgrade; old binaries ignore it, while dropping it discards transition provenance. Managed-schema migration and physical commit/readback ambiguity when a post-commit receipt read cannot reach the database remain open. Conservative chain re-dispatch does not make successor enqueue exactly-once, and batch fan-out still requires persisted dispatch-intent work.

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
