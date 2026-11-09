# Durable named continuations

`queue.Continue` adds named registered jobs to a snapshot of an existing chain or batch. Configure the original builder first. Existing closure methods and public builder interfaces keep their old behavior; mixed closures and durable hooks are rejected.

```go
q, err := queue.NewWorkerpool(
    queue.WithStore(store),
    queue.WithResourceID("billing-reports"),
)
if err != nil { return err }
q.Register("reports:build", buildReport)
q.Register("reports:complete", completeReport)
if err := q.StartWorkers(ctx); err != nil { return err }

id, err := queue.Continue(q.Chain(queue.NewJob("reports:build"))).
    OnCompletion(queue.NewJob("reports:complete").Retry(2)).Dispatch(ctx)
```

Use a stable, unique resource ID for every independently owned built-in queue resource sharing a workflow store. Every recovery worker for that resource must use the same ID, store and handler catalog. Temporal derives its resource identity from its namespace and logical queue instead and rejects `WithResourceID`.

| Hook | Trigger |
| --- | --- |
| `OnProgress` | Each committed non-canceled batch member settlement |
| `OnFailure` | First terminal member failure |
| `OnSuccess` | Successful terminal business aggregate, including an allowed-failure batch |
| `OnCompletion` | First terminal business aggregate |

Hooks run in durable trigger order, then phase order (progress, failure, success, completion), then declaration order. Only one eligible invocation executes at a time per workflow. Business work can continue while hooks run. Exhaustion settles that obligation and releases later hooks without changing business state. No hook runs from a state reader.

Hook delay is a minimum wait, not an exact execution time. Built-in coordinators measure it from trigger creation; Temporal starts the full delay when an invocation reaches the serialized ledger head. A slow predecessor can therefore add another full delay on Temporal. Do not rely on equal hook start times across drivers.

Chain trigger summaries count committed successful and failed members in `Processed`, with remaining members in `Pending`. `NextIndex` remains the count of successful nodes. An aggregate dispatch failure without a settled member does not invent a processed member.

`Message.Continuation()` returns the stable invocation identity and immutable safe trigger counters. `FindContinuations` returns triggered obligations, not pending definitions. Store the invocation ID with external effects to make application operations idempotent. Delivery is at least once: a process can die after an effect and before recording settlement.

## Built-in recovery ownership

Parent definitions and continuation definitions are accepted atomically. Business state transitions and newly triggered obligations commit together. A resource-scoped publisher starts after physical workers, claims eligible durable heads and dispatches canonical internal deliveries. Publication and execution have separate fenced leases; stale deliveries cannot steal or settle a newer attempt. Logical attempt count and retry availability live in the store, rather than relying on a broker's physical delivery count. Accepted local hook execution survives publisher cancellation.

Shutdown first stops new claims, then joins physical work. A caller timeout does not authorize destroying the database or application dependencies. Retry shutdown to join a resistant callback. Permanent recovery protocol/schema errors reach `Queue.Run` and readiness.

Memory storage provides process-local guarantees. SQL storage survives worker replacement and can recover obligations without any surviving original queue delivery. Custom stores lacking transactional continuation capability reject named binding before acceptance. Fake queues record definitions separately with `ContinuationRecords`; they do not pretend definitions have executed.

After a `busruntime.Uncommitted` redelivery disposition commits, built-in hooks reuse the logical attempt without spending an application retry. Settlement receipts prevent duplicate refunds, while each execution receives a new fenced owner generation. If execution or settlement is lost before its outcome commits, recovery must treat that result as unknown; the reserved attempt can then be consumed, as after worker loss. Retry eligibility uses the same wall clock as publication and execution leases; `WithClock` continues to supply terminal and event timestamps.

New named workflows allow at most 256 members and 32 definitions, with bounded definition bytes and a worst-case reservation of 4096 invocations and 4 MiB ledger footprint. Hooks default to a 30-second timeout. Retry is bounded to 100, timeout/backoff to one hour, delay to seven days. `UniqueFor` on a hook is rejected before acceptance. Existing ordinary and closure-based workflow limits remain unchanged.

## SQL schema rollout

The additive table is `bus_continuations_v1`. Automatic schema mode creates it on the first named binding and installs the recovery index. Legacy ordinary and closure workflows do not require it. Managed mode performs no DDL and rejects named acceptance unless the table, required columns and recovery index exist.

Apply the matching migration before deploying producers in managed mode:

```sql
-- SQLite
CREATE TABLE bus_continuations_v1 (
    workflow_id VARCHAR(255) PRIMARY KEY,
    resource_key VARCHAR(255) NOT NULL,
    ledger_json BLOB NOT NULL,
    revision BIGINT NOT NULL,
    active INTEGER NOT NULL,
    available_at_ms BIGINT NOT NULL
);
CREATE INDEX bus_continuations_v1_ready
    ON bus_continuations_v1(resource_key, active, available_at_ms);
```

PostgreSQL uses the same DDL with `BYTEA` instead of `BLOB`. MySQL uses `VARBINARY(255)` for both identity columns and `LONGBLOB` for `ledger_json`. The binary MySQL keys preserve case-sensitive resource and workflow identity. Apply the index in each dialect. The table's revision compare-and-swap and transaction locks fence competing transitions and recovery owners.

Deploy compatible workers and registrations before enabling named-hook producers. Old workers do not understand the new internal delivery and cannot recover the ledger. Keep compatible recovery workers running until every named obligation has settled before a rollback. Do not delete the additive table during rollback.

`Prune` conservatively retains named parents, receipts and member records along with their ledgers, including terminal entries. There is no automatic safe ledger cleanup policy in this version. Plan retention explicitly after quiescing producers and recovery owners, ensuring no queued delivery or application idempotency obligation can refer to removed authority. Pruning ordinary legacy workflows keeps its existing behavior.

Pruning captures bounded candidate identities before checking for named authority in the same transaction. Concurrent creation of the first continuation table cannot make a newly named parent eligible for deletion. Managed pruning does not create that additive table.

SQL connection pools must accommodate business transactions and lease renewal. Exercise actual workload concurrency when tuning pools; the integration suite tests one-connection restart recovery and competing transitions with a larger pool.
