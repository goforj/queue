# Metrics and Events Contract (Baseline)

This document defines the baseline observability contract for `queue` before GA.

For the normal root facade, it covers one event stream: `queue.Event` values delivered to the `queue.Observer` installed with `queue.WithObserver(...)`. `Event.Layer` identifies whether a fact came from queueing, worker execution, or workflow orchestration.

This is a baseline contract. Before GA, pin a version and treat field/label changes as compatibility-impacting.

## Contract Goals

- Common field names across logs/metrics/traces
- Predictable event semantics across backends
- Explicit handling of recovery/failure internals (`republish_failed`, `settlement_failed`, `process_recovered`)
- Stable labels for dashboards and alerts

## Unified Event Stream

### Queue and Worker Layers

Source:

- `queue.Observer`
- `queue.ObserverFunc`
- `queue.WithObserver(...)`

Queue, worker, and workflow events carry the same applicable dispatch/job/chain/batch correlation IDs. Internal envelope IDs are excluded from `Event.JobKey`, so telemetry grouping follows the logical application job rather than a one-off wrapper delivery. `Event.JobKey` remains an observability field rather than a persisted uniqueness key; both contracts resolve the same logical job type and payload, while uniqueness additionally includes version and effective queue framing.

Event kind type:

- `queue.EventKind`

Current runtime event kinds include:

- public dispatch lifecycle:
  - `dispatch_started`
  - `dispatch_succeeded`
  - `dispatch_failed`
- enqueue lifecycle:
  - `enqueue_accepted`
  - `enqueue_rejected`
  - `enqueue_duplicate`
  - `enqueue_canceled`
- processing lifecycle:
  - `process_started`
  - `process_succeeded`
  - `process_failed`
  - `process_retried`
  - `process_archived`
- queue control:
  - `queue_paused`
  - `queue_resumed`
- internal recovery/failure:
  - `process_recovered`
  - `republish_failed`
  - `settlement_failed`

Recommended required fields (when available):

| Field | Type | Notes |
| --- | --- | --- |
| `kind` | string | `queue.EventKind` value |
| `layer` | string | `queue`, `worker`, or `workflow` |
| `time` | timestamp | event timestamp |
| `schema_version` | integer | event envelope schema version |
| `event_id` | string | unique event identifier |
| `driver` | string | backend/runtime (`redis`, `sqs`, etc.) |
| `queue` | string | effective physical backend queue name; omitted targets currently report `default` |
| `job_type` | string | job type identifier |
| `job_key` | string | stable job key/idempotency key when available |
| `attempt` | integer | current attempt number |
| `max_retry` | integer | configured max retries |
| `scheduled` | bool | true when enqueue is scheduled/delayed |
| `duration_ms` | number | processing duration for completion/failure events |
| `error` | string | normalized error string/class for failures |

### Workflow Layer

Source:

- `queue.WithObserver(...)`
- events where `event.Layer == queue.EventLayerWorkflow`

Event kind type:

- `queue.EventKind`

Public dispatch lifecycle facts are intentionally queue-layer events because they bracket queue acceptance. They are not logical workflow transitions, even when a workflow dispatch produced them. A filter that accepts only `EventLayerWorkflow` is the narrower job, chain, batch, and callback stream.

Current workflow event kinds (via internal orchestration engine) include:

- job orchestration:
  - `job_started`
  - `job_succeeded`
  - `job_failed`
- chain lifecycle:
  - `chain_started`
  - `chain_advanced`
  - `chain_completed`
  - `chain_failed`
- batch lifecycle:
  - `batch_started`
  - `batch_progressed`
  - `batch_completed`
  - `batch_failed`
  - `batch_cancelled`
- callback lifecycle:
  - `callback_started`
  - `callback_succeeded`
  - `callback_failed`

Recommended required fields (when available):

| Field | Type | Notes |
| --- | --- | --- |
| `kind` | string | `queue.EventKind` value |
| `layer` | string | `workflow` |
| `time` | timestamp | event timestamp |
| `schema_version` | integer | workflow event schema version |
| `event_id` | string | unique workflow event identifier |
| `dispatch_id` | string | high-level dispatch correlation ID |
| `job_id` | string | workflow job record ID |
| `chain_id` | string | chain workflow ID |
| `batch_id` | string | batch workflow ID |
| `queue` | string | effective physical backend queue name; omitted targets currently report `default` |
| `job_type` | string | job type |
| `attempt` | integer | attempt number for job events |
| `duration_ms` | number | duration for succeeded/failed events |
| `error` | string | normalized error string/class for failures |

## Label/Field Naming Rules (Recommended)

Use stable, lowercase snake_case field names in logs and metrics labels.

Prefer:

- `job_type` (not `jobType`)
- `max_retry` (not `maxRetry`)
- `duration_ms` for numeric durations

For metrics dimensions, keep cardinality bounded:

- Safe/common:
  - `driver`
  - `queue`
  - `kind`
- Potentially high-cardinality (use with care):
  - `job_type`
  - `error`
  - `job_key`
  - IDs (`dispatch_id`, `job_id`, `chain_id`, `batch_id`)

## Minimum Metric Families (Baseline)

These may be implemented via logs, counters, histograms, or OTel metrics.

### Queue and Worker Metrics

- `queue_events_total{kind,driver,queue}`
- `queue_dispatch_total{kind,driver,queue}`
- `queue_process_duration_ms` histogram `{driver,queue,job_type}`
- `queue_enqueue_failures_total{driver,queue}`
- `queue_republish_failed_total{driver,queue}` (from `republish_failed`)
- `queue_settlement_failed_total{driver,queue}` (from `settlement_failed`)
- `queue_process_recovered_total{driver,queue}` (from `process_recovered`)

For `StatsCollector`, an identity-bearing `settlement_failed` closes the exact active attempt because handler execution has ended, but it increments neither `Processed` nor application `Failed`: the delivery outcome remains unresolved and may redeliver. Current built-in drivers carry the same opaque physical identity through start, process, and settlement facts. An identity-less terminal fact cannot close an identity-bearing start, and an identity-less settlement fact leaves `Active` unchanged, because event fields cannot distinguish a late settlement from a newer execution of the same job. Upgrade settlement-aware driver modules with root, and require custom drivers to forward the handler's settlement context consistently, when exact gauges matter. This deliberately conservative compatibility behavior can overcount outdated drivers instead of undercounting unrelated live work. Track `queue_settlement_failed_total` separately rather than folding it into either terminal counter.

### Workflow Metrics

- `queue_workflow_events_total{kind,queue}`
- `queue_workflow_job_duration_ms` histogram `{kind,queue,job_type}`
- `queue_workflow_chain_events_total{kind}`
- `queue_workflow_batch_events_total{kind}`

## Versioning Policy (Pre-GA Baseline)

Before GA:

- Add a visible contract version to this document (for example `v0` -> `v1`)
- Record incompatible field/semantic changes in release notes

After GA (target):

- Treat event kind removals/renames and required field changes as breaking changes
- Add deprecation periods for renamed metrics/labels where practical

## Cross-References

- `observability.go` (canonical `queue.Event`, event layers, kinds, and observer helpers)
- `runtime.go` (one `queue.WithObserver(...)` attachment path)
- `docs/ops-alerts.md` (dashboard/alert baseline)
- `docs/runbooks/` (incident response)
