# Production Configuration Guidance (Baseline)

This document provides baseline configuration guidance for `queue` in production deployments.

Treat these values as starting points. Tune them using workload measurements, integration/soak evidence, and your SLOs.

## General Principles

- Handlers should be idempotent. Durable backends may redeliver around settlement failures; Core NATS is currently ephemeral and does not provide an at-least-once queue guarantee.
- Start with conservative concurrency, then increase while watching:
  - processing latency
  - retry/failure rate
  - backlog depth
  - dependency saturation (DB/API/broker)
- Prefer explicit queue names for critical workloads (`OnQueue("critical")`) and separate worker pools per queue priority.

## Core Queue Runtime Settings

### `Workers(count)`

Use `q.WithWorkers(count)` to control worker concurrency.

Guidance:

- Start with `1-4` workers per process for new services.
- Scale horizontally first if handlers are I/O bound and dependencies are shared.
- Increase per-process workers when:
  - CPU and memory headroom exists
  - dependency limits are not saturating
  - queue backlog grows faster than workers can drain

Watch for:

- rising retries/failures after increasing workers
- downstream rate limiting
- DB connection pool exhaustion (DB backends)

### Shutdown deadlines

Shutdown drains operations and handler work already admitted to the current process. For local runtimes that includes accepted delayed workflow descendants; durable database or broker backlog remains stored for another worker or process restart. Supply a context deadline that covers the longest in-flight handler, local accepted delay, and settlement time. If the deadline expires, shutdown returns the context error; call it again with a fresh context to continue retryable cleanup.

Root operations admitted before draining hold a lifecycle lease, while new public work is rejected after draining begins. Continuations may cross that gate only while an active handler owns the same runtime-scoped permit.

Successful shutdown is terminal for that queue instance. Construct a new queue to restart processing; repeated shutdown calls remain idempotent, while a failed cleanup attempt can be retried with a fresh context.

## Job Retry / Backoff Guidance

### Retries (`Job.Retry(n)`)

Use retries for transient failures only.

Starting points:

- network/transient dependency failures: `Retry(3)` to `Retry(5)`
- user/data validation failures: no retry (return terminal failure path)

### Backoff (`Job.Backoff(d)`)

Use backoff to avoid retry storms.

Starting points:

- transient API/DB errors: `250ms` to `2s` backoff
- high-contention jobs: start around `500ms` and tune based on throughput

Notes:

- Backend behavior differs (see `docs/backend-guarantees.md`).
- Shared integration suite capability gates should be treated as the tested contract.

## Backend-Specific Guidance

### Redis (`DriverRedis`)

Good default when:

- you need a mature broker-backed queue with strong operational tooling
- Redis is already part of your stack

Starting points:

- separate queues for latency-sensitive vs bulk jobs
- monitor retry/archive churn and queue depth closely
- validate worker concurrency against Redis CPU/network and downstream dependencies

### MySQL / Postgres / SQLite (`DriverDatabase`)

Good default when:

- you want a durable queue in the same operational footprint as your app DB (MySQL/Postgres)
- local/dev simplicity matters (`sqlite`)

Starting points:

- ensure DB connection pool is sized for worker concurrency + app traffic
- keep worker concurrency modest initially (`1-4` per process)
- monitor query latency and stale-processing recovery events (`process_recovered`)

Important DB recovery knobs (`queue.Config`):

- `DatabaseProcessingRecoveryGrace`
  - grace period before reclaiming stale `processing` jobs
  - start with default unless you have proven false-positive recovery under your handler latencies
- `DatabaseProcessingLeaseNoTimeout`
  - fallback lease for jobs without explicit timeout
  - increase for very long-running jobs if you observe premature stale recovery

Every SQL processing claim has an opaque generation ID. When infrastructure keeps the row pending for the same numbered attempt, SQL normally retains inherited recovery provenance. If that delivery durably owns a new built-in workflow transition receipt and subsequent workflow infrastructure still requires redelivery, the workflow engine marks application state committed on the delivery-settlement boundary. SQL then retains the current generation rather than the older inherited generation, so the next claimant can match the receipt that actually owns the transition. The signal does not mean the queue row settled, observer callbacks ran, or continuation work completed. An application retry advances the attempt and clears every old link. Generation provenance is not an application error, does not redefine the admin-visible `last_error` field, and cannot be forged through error text.

For receipt-backed workflow recovery, pass a built-in `queue.NewSQLStore(...)` directly to `queue.WithStore`. The store writes `bus_workflow_transition_receipts` in the same transaction as a chain-node or batch-member outcome transition. Each row includes non-null integer `receipt_version` and `event_schema_version` columns, both currently `1`. The first versions the durable ownership record; the second pins the shared observer event contract and is independent from the workflow-envelope protocol version. A worker fails recovery closed on either unknown value: it returns an uncommitted error and does not acknowledge the row, execute application code, mark application state committed, or emit reconstructed facts. On stale recovery, logical receipt proof requires a complete valid persisted owner, including a nonnegative owner attempt; matching workflow kind/ID/member/incarnation and dispatch; nonempty current dispatch/`JobID`; and matching immutable job content/fingerprint. The current attempt is physical provenance and may differ from the owner or be negative. Chain physical `JobID` may also differ; batch `JobID` is its logical member key and must match. That proof suppresses handler replay. Successful member or aggregate fact reconstruction additionally requires exact `RecoveredGenerationID`, current attempt, and physical `JobID` ownership; a physical nonowner publishes no recovered success facts. `BatchCompleted` also requires that exact fact owner to own a validated aggregate terminal transition; aggregate state alone is insufficient for both one-member and multi-member batches. Built-in memory receipts survive only within the current process.

MySQL and PostgreSQL serialize the parent batch row after a member claim, and the memory store uses its existing mutex, so only one concurrent false-to-true parent transition owns the aggregate receipt and terminal effects. Every SQL aggregate row must own completion; cancellation must own a failed outcome; and a row naming the requested member must match that member receipt's workflow incarnation, complete owner, and outcome. Recovery also requires its completion and cancellation flags to agree with live terminal state. Inconsistency fails uncommitted before acknowledgement, handlers, callbacks, state-commit signaling, or facts. `TestSQLStoreBatchAggregateOwnershipMismatchFailsClosed`, `TestSQLStoreBatchAggregateIncarnationMismatchFailsClosed`, and `TestBatchRecoveryRejectsInvalidAggregateReceiptShape` cover those fail-closed branches. Real twelve-worker fail-fast races on both server dialects verify twelve member receipts, one aggregate receipt, and one failed/cancelled terminal fact pair. A separate SQLite two-member recovery scenario verifies only the completing receipt owner receives `BatchCompleted` after finalization failure.

When validated durable state proves a recovered chain predecessor succeeded and still points to its immediate successor, the runtime re-dispatches that successor without re-running the predecessor. Exact recovered generation, attempt, and physical `JobID` ownership may also reconstruct predecessor facts. A missing receipt, a custom/decorated store without receipt capability, or a logically valid receipt with different/legacy generation, different attempt, or different physical `JobID` dispatches only the successor and repeats no predecessor facts or callbacks. Supported success receipts are logically validated first: cancellation is invalid and completion must exactly match final-node position; corruption fails uncommitted with no dispatch or effects. A rejected successor enqueue is also uncommitted so recovery can retry. Treat this as at-least-once continuation recovery, not exactly-once dispatch: the runtime cannot distinguish a missing successor from one already queued but not yet progressed, so a duplicate enqueue is possible. Once the successor progresses or the chain becomes terminal, predecessor recovery does not enqueue it again.

For a receipt-backed terminal chain failure, logical receipt proof returns the first persisted `ChainState.Failure` as permanent across exact, different, or legacy recovered-generation provenance and across different attempts or physical `JobID`s; an empty persisted cause becomes a permanent diagnostic. It does not execute the handler, Catch/Finally callbacks, or logical failure facts again. Invalid version, incomplete owner, logical dispatch/job-content mismatch, workflow incarnation, outcome, or terminal flags fail closed with an uncommitted outcome; physical nonownership alone does not. Built-in `FailChain` preserves the first terminal cause, so direct store callers can no longer use a later failure call to replace it; retain secondary diagnostics separately. Receipt-absent legacy rows and custom/decorated stores may still execute application code once to preserve terminal physical classification, while duplicate workflow facts/callbacks remain suppressed. A real SQLite fixture proves repeated archive failure retains attempt zero, receipt lineage, and cause before a later `dead` settlement at attempt one; equivalent MySQL/PostgreSQL failed-chain fixtures remain open.

Receipt-backed failed batch recovery uses a generic permanent cause because the original application error is not persisted in batch state. This applies when the current delivery has a different or negative attempt, different recovered generation, or legacy provenance: the logically valid receipt still proves application failure and suppresses replay, while no replacement failure/member facts are emitted. Batch `JobID` must still match the logical member. SQL therefore drives that physical delivery to its terminal `dead` archive rather than deleting it as success, without inventing the original cause or executing the handler again. A logically valid successful batch duplicate settles without reconstructed facts under the same physical-nonowner conditions.

If a recovered SQL delivery exhausts its bounded finalization retries, the driver makes one fenced best-effort repair when the delivery did not commit new application state. A successful repair preserves the numbered attempt, restores the inherited receipt-owner generation, clears `processing_started_at`, returns the row to `pending`, and delays reclaim by the greater of the polling interval and finalization-retry floor. Real SQLite success, failed-chain, and failed-batch scenarios force at least two recovery finalization failures before a later delete or archive succeeds without handler replay. A rejected or unavailable repair is joined into `settlement_failed`; it is not a stronger durability guarantee.

Decorating a built-in store, supplying an application-defined store, or using the retained raw bus construction path hides the private receipt and response-local `claimedNow` capabilities. Those routes retain the public `WorkflowStore` contract and, when implemented, `WorkflowOutcomeStore` first-writer semantics, but they have weaker duplicate-effect and exact fact-recovery guarantees.

Transition receipts are not settlement outboxes or durable continuation intents. They do not retain observer callbacks, `Progress` closures, successor-enqueue acceptance, callback dispatch, or batch fan-out, and they cannot repair a process exit after queue finalization removed the row. Keep handlers idempotent and treat observation as best-effort until the separate durable outbox and continuation/callback-intent work lands. Custom and decorated stores still need explicit fallback contracts before they can claim the built-in receipt guarantees.

Schema migration ownership:

- queue-table startup migrations are enabled by default; set `DisableAutoMigrate: true` on `sqlitequeue.Config`, `mysqlqueue.Config`, `postgresqueue.Config`, or the advanced `queue.DatabaseConfig` when deployment tooling owns queue tables
- in managed queue mode, readiness and startup perform no queue DDL and require `queue_jobs` and `queue_unique_locks` to be base-table relations, including PostgreSQL partitioned tables, with every column the current runtime reads or writes; empty, view-backed, and incomplete schemas fail before workers poll
- managed queue validation checks presence, not write permissions, exact SQL types, constraints, or performance indexes; install the complete dialect-correct canonical schema rather than treating a successful check as a schema-lint or query-performance guarantee
- a failed managed queue check is retryable on the same runtime after deployment tooling installs or repairs the schema; canonical preprovisioned schemas are exercised through readiness, uniqueness, dispatch, and consumption on SQLite, MySQL, and PostgreSQL
- workflow-store migration policy is constructor-selected: `queue.NewSQLStore` preserves legacy migration-on-first-use behavior, including when compatibility field `SQLStoreConfig.AutoMigrate` is false; `queue.NewSQLStoreWithManagedSchema` performs no workflow DDL
- when deployment tooling owns workflow schema, create every dialect-correct workflow table before constructing the store with `queue.NewSQLStoreWithManagedSchema`; `bus_workflow_transition_receipts` must include non-null `receipt_version` and `event_schema_version` integer columns as well as its ownership fields
- keep either migration-on-start default only when the runtime identity has DDL permission and concurrent application startup is coordinated
- a failed queue migration can be retried by a later `Start`; a workflow-store first-use migration failure remains attached to that store instance, so correct the lock, permission, or connectivity issue and construct a new store
- a wholly fresh MySQL auto-schema creates workflow/member and receipt identities as `VARBINARY(255)` and callback keys as `VARBINARY(512)`
- when legacy MySQL state tables exist but the receipt table is missing, ordinary `queue.NewSQLStore` validates their `VARBINARY` keys and derives receipt `workflow_id` from the larger effective chain-or-batch ID capacity and `member_id` from the larger chain-node-or-batch-job capacity; `TestWorkflowStoreIntegration_MySQLAutoMigratesMissingReceiptAtLegacyWidths` proves the real 512/512 upgrade path with long identities
- automatic startup never alters an existing receipt table; its live widths participate in capacity discovery, so quiesce workflow writers and use an operator-managed migration before starting a new store when that table is missing columns, uses incompatible identity types, or is narrower than the capacities the deployment must retain
- a derived receipt primary key can exceed the MySQL server's indexed-key budget when established identity widths are extreme; startup then fails with both derived widths and schema-first guidance instead of narrowing or altering live tables, and operators must precreate a compatible indexed receipt schema or explicitly migrate supported identity limits and existing data
- all MySQL workflow identity columns, including receipt identities, must use byte-exact `VARBINARY`; incompatible types fail schema-capacity discovery, while a managed SQLite/PostgreSQL schema encounters a missing receipt table when a receipt operation first runs
- the current built-in pruner removes transition receipts with their terminal parent workflow
- use a schema-first, quiescent worker rollout for the receipt table until cross-dialect migration concurrency evidence is complete
- for rollback, quiesce new workers before starting old binaries and leave the additive table in place; old code ignores it, dropping it destroys provenance, and an old pruner can leave receipt rows that it does not know how to delete
- real SQLite, MySQL, and PostgreSQL finalization-failure tests cover auto-schema receipt creation, supported-version read/write, exact-owner recovery, and no handler re-execution; MySQL and PostgreSQL additionally cover concurrent aggregate ownership
- managed-schema rollout/rollback, real cross-dialect pruning, and physical commit/readback ambiguity when the database or context is unavailable still require separate gates

Deriving a missing MySQL receipt table preserves established wider `VARBINARY` capacities without a source/API, configuration-file, workflow-envelope, or minimum-Go-version change. A pre-existing incompatible receipt table, or live widths whose derived primary key exceeds the server budget, still requires a persisted-schema and operational migration; quiesce workers and audit existing identities before changing those limits.

When tuning:

- longer leases reduce false-positive recovery
- shorter leases reduce time-to-recovery after worker crashes
- validate changes with crash/restart scenarios and soak runs

### NATS (`DriverNATS`)

Good default when:

- you already operate NATS and want lightweight broker integration

Starting points:

- use Core NATS only where ephemeral broadcast delivery is acceptable; this adapter is not a durable competing-consumer work queue
- use conservative concurrency while validating duplicate/ordering expectations
- do not rely on delayed/retry survival across disconnect, process shutdown, or periods with no subscriber

### SQS (`DriverSQS`)

Good default when:

- you are on AWS and want managed queue infrastructure

Starting points:

- partition critical and bulk jobs into separate queues
- validate handler duration vs SQS visibility timeout behavior in your environment
- size visibility for sequential processing of a received batch; workers do not yet extend visibility while handlers run
- monitor duplicate deliveries and end-to-end latency under retries

Operational note:

- Local integration tests use LocalStack; production behavior must still be validated in AWS.

### RabbitMQ (`DriverRabbitMQ`)

Good default when:

- RabbitMQ is already a first-class platform dependency

Starting points:

- separate queues by priority/workload class
- validate restart/retry behavior and throughput under your expected publish/consume rate
- watch connection/channel health and reconnect churn

Operational note:

- workers do not currently reconnect after their delivery channel closes; replace the runtime after connection loss
- AMQP dialing and resource closure may exceed a lifecycle context deadline, so supervise shutdown at the process level

## Queue Layout Recommendations

Use multiple queues when workloads differ materially by:

- latency sensitivity
- expected runtime
- retry behavior
- dependency target (for blast-radius isolation)

Example layout:

- `critical`
- `default`
- `bulk`

Run dedicated workers (or worker pools) per queue class when needed.

## Observability Hooks (Enable Early)

At minimum, wire:

- one observer with `queue.WithObserver(...)` for queue, worker, and workflow events

`queue.Config.Observer` remains a compatibility path and feeds the same event stream, but new applications should prefer the constructor option consistently across drivers.

Track and alert on:

- `process_failed`
- `process_retried`
- `republish_failed`
- `settlement_failed`
- `process_recovered` (DB backends)

See:

- `docs/metrics-contract.md`
- `docs/ops-alerts.md`
- `docs/runbooks/`

## Rollout Checklist (Per Service)

- Start with low concurrency and one or two queues
- Enable observers and dashboards before production traffic
- Run a canary deployment
- Verify backlog, latency, retry rate, and duplicate behavior
- Increase concurrency gradually
- Record final chosen values and rationale

## GA Completion Criteria for This Document

Before GA, expand this baseline with:

- backend-specific recommended ranges derived from soak/benchmark evidence
- SQS visibility timeout guidance with concrete examples
- DB connection pool sizing examples tied to worker concurrency
- links to production dashboards and runbooks
