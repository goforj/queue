# Queue Events Contract

This document defines the public observability event contract emitted through `Observer`.

## Goals

- Keep event names stable for integrations (logging, metrics, tracing, dashboards).
- Keep semantics consistent across drivers.
- Avoid exposing sensitive payload data by default.

## Event kinds

Enqueue lifecycle:

- `EventEnqueueAccepted`: task accepted for enqueue.
- `EventEnqueueRejected`: enqueue failed with error.
- `EventEnqueueDuplicate`: enqueue rejected as duplicate (`UniqueFor`).
- `EventEnqueueCanceled`: enqueue canceled by context.

Processing lifecycle:

- `EventProcessStarted`: handler attempt started.
- `EventProcessSucceeded`: handler attempt succeeded.
- `EventProcessFailed`: handler attempt failed.
- `EventProcessRetried`: failed attempt scheduled for retry.
- `EventProcessArchived`: terminal failure (no retries left).

Queue control lifecycle:

- `EventQueuePaused`: queue consumption paused.
- `EventQueueResumed`: queue consumption resumed.

## Required fields

Present on all events whenever known:

- `Kind`
- `Time`
- `Driver`
- `Queue`
- `TaskType`
- `TaskKey`

Processing events additionally include:

- `Attempt`
- `MaxRetry`
- `Duration` (for `Succeeded` and `Failed`)

Failure/cancel/reject events additionally include:

- `Err`

## Semantics and guarantees

- Events are per-attempt, not aggregated.
- `EventProcessRetried` is emitted only when another attempt will occur (`Attempt < MaxRetry`).
- `EventProcessArchived` is emitted when retries are exhausted.
- `TaskKey` is a deterministic hash key for correlation. It is not guaranteed globally unique.
- `Queue` defaults to `"default"` when not explicitly set.

## Cross-driver support

Guaranteed across all drivers:

- Enqueue lifecycle events
- Processing lifecycle events (as supported by each runtime path)

Driver-specific capabilities:

- Native snapshot stats: currently supported by Sync, Workerpool, Database, Redis.
- Pause/resume control: currently supported by Sync, Workerpool, Redis.
- Other drivers still emit collector-based events when `Observer` is configured.

## Observer behavior contract

- Observers are side-effect hooks only; they must not control queue execution.
- Queue processing must continue even if an observer is slow, fails, or panics.
- Implementations should prefer non-blocking observer behavior in hot paths.
- Logging adapters should avoid raw payload logging by default.

## Versioning

- `EventKind` names and baseline semantics are public API.
- Additive changes are allowed (new event kinds, new optional fields).
- Breaking changes require a major version bump.
