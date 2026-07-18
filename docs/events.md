# Queue Events Contract

This document defines the root application facade's unified observability contract emitted through `queue.Observer`. `Event.Layer` distinguishes queue, worker, and workflow facts without requiring separate observer models on the normal `*queue.Queue` path. The public `bus` package still exposes its legacy compatibility event model until the forwarding-facade migration in `plan.md` M2-07 is complete.

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

Positive job, chain, batch, and callback facts use the same SQL/SQS/RabbitMQ settlement boundary as `EventProcessSucceeded`. Callback redelivery after an at-most-once marker emits no second success fact; closure callbacks remain explicitly ephemeral and can be lost with their owning process.

An allowed batch item failure emits `EventBatchProgressed` with `Err` and does not emit `EventBatchFailed`. The aggregate can later emit `EventBatchCompleted` after every item reaches an allowed terminal outcome. `EventBatchFailed` is reserved for a non-allowed failure or cancellation path that makes the aggregate fail.

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

Every layer includes the applicable `DispatchID`, `JobID`, `ChainID`, and `BatchID` correlation fields when the delivery carries workflow metadata. Queue and worker facts decode the current versioned internal envelope so they can be joined to workflow facts without inspecting payloads in application observers.

## Semantics and guarantees

- Events are per-attempt, not aggregated.
- Dispatch, enqueue, and queue-control events use `EventLayerQueue`; physical attempt events use `EventLayerWorker`; logical job, chain, batch, and callback transitions use `EventLayerWorkflow`.
- `EventProcessRetried` is emitted when processing begins with `Attempt > 0`. It is intentionally not emitted merely because a handler returned an error, and consumers must tolerate a repeated fact when infrastructure redelivers the same numbered attempt.
- `EventProcessArchived` is reserved for a driver-confirmed terminal settlement; drivers that cannot yet confirm that boundary omit it rather than emitting a prediction.
- `JobKey` is a deterministic hash of the logical job type and payload. Volatile dispatch/workflow IDs are excluded, and the value is not guaranteed globally unique.
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
- Additive changes are allowed (new event kinds, new optional fields).
- Breaking changes require an explicit compatibility release and migration guide; after v1 they require a major version bump.

## Unified observer migration

The observer collapse is an explicit pre-v1 compatibility boundary:

- `queue.WithObserver` accepts `queue.Observer` and receives queue, worker, and workflow layers.
- `queue.WorkflowEvent`, `queue.WorkflowEventKind`, `queue.WorkflowObserver`, and `queue.WorkflowObserverFunc` are deprecated aliases of the root event model.
- Code that used unkeyed `queue.Event` or `bus.Event` literals must switch to keyed literals because the envelopes now include correlation fields.
- A custom `bus.Observer` passed to the root option must be adapted with `queue.ObserverFunc`. Direct legacy `bus` consumers can continue using `bus.WithObserver` during the facade migration.
- Existing workflow-only sinks can retain their previous scope by returning early unless `event.Layer == queue.EventLayerWorkflow`.

This migration changes Go source compatibility and observer volume/concurrency. It does not change persisted workflow records or queue wire envelopes.
