# Operations Alerts and Dashboards (Baseline)

This document defines a minimal alert/dashboard baseline for production queue deployments.

Use this as a starting point and tune thresholds per backend, workload, and SLOs.

## Scope

- Queue runtime health (workers, backlog, throughput, latency)
- Failure/retry behavior
- Recovery/failure events emitted by queue internals
- Backend-specific symptoms (broker/database connectivity)

## Core Dashboard Panels

Create at least one dashboard with these panels per environment and backend.

### 1. Queue Depth / Backlog

- Pending jobs by queue
- Scheduled/delayed jobs by queue
- Failed/archived jobs by queue (if backend exposes this)

Why:

- Detect backlog growth early
- Distinguish delayed load vs unhealthy accumulation

### 2. Throughput

- Jobs started/sec
- Jobs succeeded/sec
- Jobs failed/sec
- Retries scheduled/sec

Why:

- Confirm workers are making progress
- Catch retry churn and regressions

### 3. Latency

- Processing duration P50/P95/P99 by job type and queue
- End-to-end completion latency (enqueue to success) if available

Why:

- Detect handler/dependency regressions
- Validate capacity/concurrency assumptions

### 4. Worker Health

- Worker count / instance count
- Worker liveness/readiness failures
- Restart count

Why:

- Detect crash loops, stuck workers, rollout issues

### 5. Queue Event Counters (Operational)

Track counts/rates for runtime events (from `queue.Observer`), including:

- `process_started`
- `process_succeeded`
- `process_failed`
- `process_retried`
- `process_archived`
- `republish_failed`
- `process_recovered`

Why:

- `republish_failed` exposes hidden delay/retry republish churn
- `process_recovered` shows DB stale-processing recovery activity

## Minimum Alerts (Baseline)

Thresholds below are placeholders. Tune them to workload and expected traffic.

### A. Backlog Growth Alert

Fire when all are true for a sustained window (for example 10m):

- pending backlog > configured threshold
- backlog slope is positive
- success throughput does not increase proportionally

Suggested severity:

- warning at moderate threshold
- critical at sustained higher threshold

### B. Worker No-Progress Alert

Fire when:

- workers are healthy/running
- but `process_succeeded` rate is near zero
- and pending backlog is non-zero

Why:

- Catches stuck workers and dependency deadlocks not visible via liveness only

### C. Failure/Retry Churn Alert

Fire when one or both are elevated:

- `process_failed` rate
- `process_retried` rate

Dimension by:

- queue
- job type

Why:

- Quickly isolates poison-message or dependency incidents

### D. Republish Failure Alert

Fire when `republish_failed` is non-zero above a low threshold over a short window.

Suggested default posture:

- warning on any sustained non-zero rate
- critical if rising during backlog growth or broker instability

### E. Stale Processing Recovery Alert (DB Backends)

Fire when `process_recovered` rate exceeds expected baseline.

Why:

- A rising rate indicates worker crashes, DB finalization failures, or unhealthy pods causing stale `processing` rows

### F. Worker Crash Loop / Restart Alert

Fire when worker restart count exceeds threshold over a short window.

Why:

- Often precedes backlog incidents and duplicate/recovery churn

## Event Semantics to Document/Standardize

At minimum, document fields/labels for:

- `queue`
- `driver`
- `job_type`
- `attempt`
- `max_retry`
- `error` (or normalized error class if available)

Events that must be covered:

- `republish_failed`
- `process_recovered`

See also:

- `observability.go` (event kinds)
- `docs/runbooks/*.md` (incident response)

## Backend Notes (Baseline Expectations)

- `redis` / `rabbitmq` / `nats` / `sqs`:
  - prioritize broker connectivity, retry churn, and backlog/throughput panels
- `mysql` / `postgres` / `sqlite`:
  - prioritize stale-processing recovery visibility (`process_recovered`) and DB latency/connection health

## GA Completion Criteria for This Document

Before GA, replace placeholders with:

- environment-specific thresholds (warning/critical)
- dashboard links
- alert names/routes (PagerDuty/Slack/etc.)
- ownership/on-call escalation path
