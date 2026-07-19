# Queue Events Contract

This document defines the root application facade's unified observability contract emitted through `queue.Observer`. `Event.Layer` distinguishes queue, worker, and workflow facts without requiring separate observer models on the normal `*queue.Queue` path. The deprecated `bus` package retains its legacy event shape only as an adapter at the compatibility boundary; it no longer owns a second event producer or orchestration engine.

## Goals

- Keep event names stable for integrations (logging, metrics, tracing, dashboards).
- Keep semantics consistent across drivers.
- Avoid exposing sensitive payload data by default.

## Event kinds

Queue dispatch lifecycle:

- `EventDispatchStarted`: public dispatch began.
- `EventDispatchSucceeded`: public dispatch crossed the backend acceptance boundary. A synchronous handler can still return an application error after this fact.
- `EventDispatchFailed`: public dispatch failed before acceptance.
- `EventEnqueueAccepted`: job accepted for dispatch.
- `EventEnqueueRejected`: dispatch failed with error.
- `EventEnqueueDuplicate`: dispatch rejected as duplicate (`UniqueFor`).
- `EventEnqueueCanceled`: dispatch canceled by context.

Processing lifecycle:

- `EventProcessStarted`: handler attempt started.
- `EventProcessSucceeded`: handler attempt succeeded. SQL, SQS, and RabbitMQ emit this only after durable row finalization, deletion, or acknowledgement respectively; backends without a post-handler settlement hook retain their documented weaker boundary.
- `EventProcessFailed`: handler attempt failed.
- `EventProcessRetried`: processing began for a numbered application retry attempt. Infrastructure redelivery of that same attempt may repeat the fact.
- `EventProcessArchived`: the driver confirmed terminal settlement for a failed attempt.
- `EventRepublishFailed`: an internal delay or retry replacement could not be published.
- `EventSettlementFailed`: durable SQL finalization, broker acknowledgement, or broker deletion failed after handler or replacement work completed, so redelivery remains possible.

Queue control lifecycle:

- `EventQueuePaused`: queue consumption paused.
- `EventQueueResumed`: queue consumption resumed.

Workflow lifecycle:

- `EventJobStarted`, `EventJobSucceeded`, `EventJobFailed`
- `EventChainStarted`, `EventChainAdvanced`, `EventChainCompleted`, `EventChainFailed`
- `EventBatchStarted`, `EventBatchProgressed`, `EventBatchCompleted`, `EventBatchFailed`, `EventBatchCancelled`
- `EventCallbackStarted`, `EventCallbackSucceeded`, `EventCallbackFailed`

Positive job, chain, batch, and callback facts use the same SQL/SQS/RabbitMQ settlement boundary as `EventProcessSucceeded`. The SQL queue gives every processing claim an opaque generation ID. Same-attempt infrastructure redelivery normally retains inherited unsettled-generation provenance. When the current generation commits a receipt-backed workflow transition before later infrastructure work requests redelivery, the workflow engine marks application state committed and SQL retains that current generation instead. The signal selects the truthful receipt owner; it does not commit deferred facts or prove observer delivery. An application retry increments the attempt and clears the link, while recovery flags, aggregate state, and application error text do not supply equivalent authority.

The direct built-in workflow-store path records a separate transition receipt in the same mutation that commits a chain-node or batch-member outcome. `receipt_version` identifies the durable ownership format and `event_schema_version` identifies the shared observer fact contract it can reconstruct; both are currently `1`. The event schema is independent from the workflow-envelope protocol even while their current numeric values match. An unsupported receipt or event-schema version fails recovery closed with an uncommitted error: the worker does not acknowledge the delivery, run application code, mark application state committed, or publish reconstructed facts. Logical receipt proof requires supported versions; a complete valid persisted owner, including a nonnegative owner attempt; matching workflow kind/ID/member/incarnation and logical dispatch; nonempty current dispatch/`JobID`; and matching immutable job content/fingerprint. The current attempt is physical provenance and may differ from the owner or be negative. Chain physical `JobID` may also differ; batch `JobID` is its logical member key and must match. That proof suppresses duplicate handler execution. Successful fact reconstruction additionally requires exact prior `RecoveredGenerationID`, current attempt, and physical `JobID` ownership. A physical nonowner publishes no recovered success fact. An exact owner defers the already-committed `EventJobSucceeded` plus `EventChainAdvanced`, `EventChainCompleted`, or `EventBatchProgressed` fact to the reclaimed delivery's new settlement. Reconstructed successes use zero duration because the original handler timing is not persisted. Their deterministic `EventID` permits consumer deduplication, but observer invocation may repeat. Real SQLite, MySQL, and PostgreSQL finalization-failure scenarios cover successful terminal-chain recovery through their physical receipt schemas.

A logically valid failed chain receipt returns the first persisted `ChainState.Failure` as a permanent physical outcome across exact, different, or legacy recovered-generation provenance and across different attempts or physical `JobID`s; an empty cause becomes a permanent terminal diagnostic. Recovery does not re-run the handler, Catch/Finally callbacks, `EventJobFailed`, or `EventChainFailed`; those facts remain occurrence-based and require the still-open settlement outbox if they must survive a post-transition finalization crash. Invalid version, incomplete owner, logical dispatch/job-content mismatch, workflow incarnation, outcome, or terminal flags fail closed before application code; physical nonownership alone does not. Receipt-absent legacy rows and stores without the private capability retain weaker replay behavior: application code may run once to recover terminal physical classification, but duplicate workflow facts and callbacks remain suppressed. The built-in stores preserve the first terminal chain cause so a later `FailChain` call cannot change what recovery returns. A real SQLite fixture proves repeated archive failure retains the cause and receipt lineage before one final `dead` settlement; server-dialect failed-chain fixtures remain open.

`EventBatchCompleted` is reconstructed only when a validated aggregate receipt says the exact recovered generation, current attempt, and `JobID` made that batch terminal and live state agrees. Batch `JobID` is also the logical member key, so it must match even when only suppressing replay. Every SQL aggregate row must own completion, cancellation must own a failed outcome, and a row naming the requested member must match that member receipt's workflow incarnation, complete owner, and outcome. Cancellation and completion flags must also agree with live terminal state; corruption fails uncommitted before any partial member or aggregate fact. The aggregate proof works independently of batch size. Built-in memory settlement serializes the parent transition under one mutex; MySQL and PostgreSQL lock the parent batch row after each member claim, so only the first false-to-true terminal transition owns aggregate facts. `TestSQLStoreBatchAggregateOwnershipMismatchFailsClosed`, `TestSQLStoreBatchAggregateIncarnationMismatchFailsClosed`, and `TestBatchRecoveryRejectsInvalidAggregateReceiptShape` pin these constraints. Real twelve-member fail-fast races on both server dialects prove one aggregate-owner receipt and one `EventBatchFailed`/`EventBatchCancelled` pair. The real SQLite two-member terminal-owner scenario verifies that recovery emits completion with the completing member's `JobID`, does not re-execute either handler, and never infers ownership from the earlier member or aggregate state alone. A built-in store's `claimedNow` result suppresses duplicate facts and effects only for the immediate call and is not persisted proof. Custom stores, decorators around built-ins, and the retained raw bus construction path do not expose the private receipt contract, so they do not have this exact fact-recovery guarantee.

A recovered chain predecessor whose validated durable state proves success re-dispatches its immediate successor while live state still points to that successor, without re-running the predecessor. Exact recovered generation, attempt, and physical `JobID` ownership can reconstruct the predecessor facts above; a missing receipt, a store without receipt capability, or a logically valid physical nonowner dispatches only the successor and emits no predecessor facts or callbacks. A supported success receipt is logically validated before liveness recovery: cancellation is invalid and completion must exactly match final-node position, otherwise recovery is uncommitted with no dispatch or effects. Progressed and terminal state is a no-op. This closes definite enqueue rejection and the legacy/custom liveness gap but does not make continuation delivery exactly-once: a successor already queued but not yet progressed is indistinguishable from a missing successor, so duplicate enqueue remains possible under the queue's at-least-once contract.

A workflow transition receipt is not a settlement receipt or observer outbox. Fact recovery does not retain `Progress` closures, successor-enqueue acceptance, batch fan-out, or terminal callbacks. If a recovered SQL delivery's finalization fails again, the driver makes a fenced best-effort attempt to restore the inherited receipt-owner generation on the same attempt, return the row to `pending`, and delay the next reclaim. Real SQLite tests force multiple such failures before later success, failed-chain archive, or failed-batch archive without replaying application code. If queue finalization commits and the process exits before deferred observer calls, no recoverable queue row may remain. A durable settlement outbox with restart draining and persisted continuation/callback intents are still required; observers remain best-effort telemetry rather than workflow continuation machinery. Callback redelivery after an at-most-once marker emits no second success fact, and closure callbacks remain explicitly ephemeral.

An allowed batch item failure emits `EventBatchProgressed` with `Err` and does not emit `EventBatchFailed`. The aggregate can later emit `EventBatchCompleted` after every item reaches an allowed terminal outcome. `EventBatchFailed` is reserved for a non-allowed failure or cancellation path that makes the aggregate fail. Batch settlement durably owns the member's success-or-failure category, but the established batch state does not persist a per-member error string. A same-call ambiguous workflow-store commit can resolve category ownership from a matching receipt while retaining that physical attempt's error detail when readback remains available. On restart, a logically valid failed receipt returns a generic permanent cause even across a different attempt, different or legacy recovered-generation provenance, so SQL archives the delivery as `dead` instead of acknowledging it as success. Batch `JobID` must still match the logical member. The recovery does not fabricate the missing original cause or emit replacement failure/member facts. Physical commit/readback ambiguity that cannot reach the database remains unresolved.

## Required fields

Present on all events whenever known:

- `Kind`
- `Layer`
- `Time`
- `SchemaVersion`
- `EventID`
- `Driver`
- `Queue`
- `JobType`
- `JobKey`

Processing events additionally include:

- `Attempt`
- `MaxRetry`
- `Duration` (for `Succeeded` and `Failed`)

Failure/cancel/reject events additionally include:

- `Err`

Every layer includes the applicable `DispatchID`, `JobID`, `ChainID`, and `BatchID` correlation fields when the delivery carries supported metadata. Queue and worker facts read the versioned direct-driver sidecar or decode a retained workflow envelope, so they can be joined to workflow facts without inspecting payloads in application observers.

## Semantics and guarantees

- Events are per-attempt, not aggregated.
- Dispatch, enqueue, and queue-control events use `EventLayerQueue`; physical attempt events use `EventLayerWorker`; logical job, chain, batch, and callback transitions use `EventLayerWorkflow`.
- `EventProcessRetried` is emitted when processing begins with `Attempt > 0`. It is intentionally not emitted merely because a handler returned an error, and consumers must tolerate a repeated fact when infrastructure redelivers the same numbered attempt.
- `EventProcessArchived` is reserved for a driver-confirmed terminal settlement; drivers that cannot yet confirm that boundary omit it rather than emitting a prediction.
- `JobKey` is a deterministic hash of the logical job type and payload. Volatile dispatch/workflow IDs are excluded, and the value is not guaranteed globally unique.
- Correlated recoverable job successes and emitted positive chain or batch transition facts use a deterministic `EventID` for the same logical fact across settlement recovery. Failure EventIDs remain occurrence-based. Deterministic identity supports deduplication; it does not prove that an observer received the fact or make every event exactly-once.
- `Queue` defaults to `"default"` when not explicitly set.
- Aggregate and callback workflow facts retain the triggering job's effective queue, logical job type, and `JobKey`, so observers can join them to queue and worker facts without reading payloads.

## Cross-driver support

Guaranteed across all drivers:

- Dispatch lifecycle events
- Processing lifecycle events (as supported by each runtime path)

Driver-specific capabilities:

- Native snapshot stats: currently supported by Sync, Workerpool, Database, Redis.
- Pause/resume control: currently supported by Sync, Workerpool, Redis.
- Other drivers still emit collector-based events when `Observer` is configured.

## Observer behavior contract

- Observers are best-effort telemetry hooks only; they must not control queue execution or implement workflow continuations.
- Observer calls are synchronous and causally ordered on an individual execution path. Slow observers therefore delay that path.
- Dispatchers and workers may invoke the same observer concurrently. Observer implementations must synchronize mutable state they own.
- Observer panics are isolated and do not change queue or workflow outcomes.
- Use `ChannelObserver` when asynchronous delivery or an explicit drop-if-full policy is required.
- Logging adapters should avoid raw payload logging by default.

## Versioning

- `EventKind` names and baseline semantics are public API.
- `Event.SchemaVersion` identifies the canonical observer envelope shared by queue, worker, and workflow layers. It is not the workflow-envelope protocol version and may evolve independently.
- Additive changes are allowed (new event kinds, new optional fields).
- Breaking changes require an explicit compatibility release and migration guide; after v1 they require a major version bump.

## Unified observer migration

The observer collapse is an explicit pre-v1 compatibility boundary:

- `queue.WithObserver` accepts `queue.Observer` and receives queue, worker, and workflow layers.
- `queue.WorkflowEvent`, `queue.WorkflowEventKind`, `queue.WorkflowObserver`, and `queue.WorkflowObserverFunc` are deprecated aliases of the root event model.
- Code that used unkeyed `queue.Event` or `bus.Event` literals must switch to keyed literals because the envelopes now include correlation fields.
- Adapt custom `bus.Observer` implementations with `queue.ObserverFunc` when constructing a root queue. `bus.WithObserver` remains supported only on the retained raw-`busruntime.Runtime` construction route; an already-built `*queue.Queue` must receive observation options when it is constructed.
- Sinks that only need logical job, chain, batch, and callback transitions can return early unless `event.Layer == queue.EventLayerWorkflow`.
- Legacy `queue.WorkflowObserver` and `bus.Observer` sinks also received `EventDispatchStarted`, `EventDispatchSucceeded`, and `EventDispatchFailed`. Those dispatch facts deliberately belong to `EventLayerQueue` in the unified model because they describe public queue acceptance, not a committed workflow transition. To retain the full legacy scope, accept the workflow layer plus those three event kinds:

```go
if event.Layer != queue.EventLayerWorkflow {
	switch event.Kind {
	case queue.EventDispatchStarted,
		queue.EventDispatchSucceeded,
		queue.EventDispatchFailed:
	default:
		return
	}
}
```

This migration changes Go source compatibility and observer volume/concurrency. It does not change persisted workflow records or queue wire envelopes.
