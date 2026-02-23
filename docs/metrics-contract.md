# Metrics and Events Contract (Baseline)

This document defines the baseline observability contract for `queue` before GA.

It covers two event streams:

- Queue runtime events (`queue.Event`) via `queue.Observer`
- Workflow/runtime orchestration events (`queue.WorkflowEvent`) via `queue.WithObserver(...)`

This is a baseline contract. Before GA, pin a version and treat field/label changes as compatibility-impacting.

## Contract Goals

- Common field names across logs/metrics/traces
- Predictable event semantics across backends
- Explicit handling of recovery/failure internals (`republish_failed`, `process_recovered`)
- Stable labels for dashboards and alerts

## Event Streams

### 1. Queue Runtime Events (`queue.Event`)

Source:

- `queue.Observer`
- `queue.ObserverFunc`
- `queue.Config.Observer`

Event kind type:

- `queue.EventKind`

Current runtime event kinds include:

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

Recommended required fields (when available):

| Field | Type | Notes |
| --- | --- | --- |
| `kind` | string | `queue.EventKind` value |
| `time` | timestamp | event timestamp |
| `driver` | string | backend/runtime (`redis`, `sqs`, etc.) |
| `queue` | string | logical/physical queue name in runtime context |
| `job_type` | string | job type identifier |
| `job_key` | string | stable job key/idempotency key when available |
| `attempt` | integer | current attempt number |
| `max_retry` | integer | configured max retries |
| `scheduled` | bool | true when enqueue is scheduled/delayed |
| `duration_ms` | number | processing duration for completion/failure events |
| `error` | string | normalized error string/class for failures |

### 2. Workflow Events (`queue.WorkflowEvent`)

Source:

- `queue.WithObserver(...)`
- `queue.WorkflowObserver`
- `queue.WorkflowObserverFunc`

Event kind type:

- `queue.WorkflowEventKind`

Current workflow event kinds (via internal orchestration engine) include:

- dispatch:
  - `dispatch_started`
  - `dispatch_succeeded`
  - `dispatch_failed`
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
| `kind` | string | `queue.WorkflowEventKind` value |
| `time` | timestamp | event timestamp |
| `schema_version` | integer | workflow event schema version |
| `event_id` | string | unique workflow event identifier |
| `dispatch_id` | string | high-level dispatch correlation ID |
| `job_id` | string | workflow job record ID |
| `chain_id` | string | chain workflow ID |
| `batch_id` | string | batch workflow ID |
| `queue` | string | target queue |
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

### Queue Runtime Metrics

- `queue_events_total{kind,driver,queue}`
- `queue_process_duration_ms` histogram `{driver,queue,job_type}`
- `queue_enqueue_failures_total{driver,queue}`
- `queue_republish_failed_total{driver,queue}` (from `republish_failed`)
- `queue_process_recovered_total{driver,queue}` (from `process_recovered`)

### Workflow Metrics

- `queue_workflow_events_total{kind,queue}`
- `queue_workflow_dispatch_total{kind,queue}`
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

- `observability.go` (queue runtime event kinds and `queue.Event`)
- `runtime.go` (workflow aliases: `queue.WorkflowEvent`, `queue.WorkflowEventKind`)
- `bus/events.go` (underlying workflow event schema)
- `docs/ops-alerts.md` (dashboard/alert baseline)
- `docs/runbooks/` (incident response)
