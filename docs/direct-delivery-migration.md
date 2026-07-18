# Direct Delivery Migration

## Contract

`Queue.Dispatch` sends an ordinary job using its application `Job.Type` and exact `Job.PayloadBytes()`. It does not create a workflow envelope. Correlation travels beside the job through versioned driver metadata:

```json
{
  "schema_version": 1,
  "dispatch_id": "dsp_...",
  "job_id": "job_...",
  "queue": "critical"
}
```

`chain_id` and `batch_id` are reserved in the same record for correlated delivery kinds. Direct jobs leave them empty. The application payload is never nested inside this record.

Drivers carry the record using their native transport boundary:

- Sync and Workerpool retain it privately on the in-memory `Job` value.
- Redis stores JSON in the `goforj-queue-driver-job-metadata` Asynq header.
- NATS, SQS, and RabbitMQ add an optional `metadata` member to their existing transport message.
- SQL stores JSON in the nullable `queue_jobs.metadata_json` column.

Missing metadata is valid for legacy and low-level deliveries. Version 1 is trusted. Malformed or unknown versions never block application delivery and never supply correlation IDs; workers fall back to the physical application identity or decode a supported version-one workflow envelope.

Chains, batches, and ephemeral callbacks continue to use the version-one workflow envelope because their durable state transitions require orchestration fields. The raw `bus.New(busruntime.Runtime)` compatibility route also retains its exact version-one `bus:job` bytes. New workers keep all four legacy handlers registered, so old backlog remains readable.

Application job types equal to `bus:job`, `bus:chain:node`, `bus:batch:job`, or `bus:callback` continue through the legacy direct envelope. This prevents an application registration from replacing a reserved workflow handler.

## Deployment Order

Compatibility is intentionally one-way: a new worker reads old envelopes and new direct deliveries, while an old worker does not understand a new application-type delivery. Use this expand-and-contract rollout:

1. For externally managed SQL schemas, add nullable `metadata_json TEXT` to `queue_jobs`. Automatic migration adds it when enabled. An intentionally empty schema remains untouched when migration is disabled; an existing table without the column fails worker startup with a specific error.
2. Deploy the new worker-capable release while producers include `queue.WithLegacyDirectEnvelope()`. This keeps all producers on `bus:job` until every consumer has been replaced.
3. Verify no old consumers remain for the target queues.
4. Remove `WithLegacyDirectEnvelope` from producers to enable canonical direct delivery.
5. Before rolling workers back, restore legacy producer emission and drain every direct-delivery backlog. Only then may old workers return.

Do not run old and new consumers after step 4. The failure mode differs by backend: SQL can consume retries and dead-letter an unknown application type; SQS and RabbitMQ can delete or acknowledge it; Redis can retry and archive it; Core NATS can drop it and its broadcast model can also duplicate work during overlapping consumer cutovers. Core NATS therefore requires a coordinated consumer/producer switch rather than a durability claim.

## Compatibility Classification

- **Source/API:** Existing root and `bus` calls retain their signatures. Driver metadata helpers and `WithLegacyDirectEnvelope` are additive advanced APIs.
- **Configuration:** Existing configuration remains valid. The migration option is temporary and opt-in.
- **Persisted data:** SQL adds one nullable column. Existing rows remain readable and retain `NULL`; old producers can continue inserting rows that omit the column after migration.
- **Wire:** New root direct deliveries use the application type and payload plus optional transport metadata. Legacy workflow envelopes remain readable and raw-runtime `bus` emission remains byte-stable.
- **Runtime behavior:** Exact `Job.PayloadBytes()` are delivered without a JSON re-marshal. Arbitrary bytes now reach the handler; `Message.Bind` reports a JSON error only if the application chooses to bind non-JSON bytes. An absent payload remains absent instead of becoming literal JSON `null`.
- **Operations:** Worker-first rollout and backlog-aware rollback are required as described above.
- **Minimum Go version:** Unchanged.

Logical uniqueness remains on the version-one queue/type/payload identity. Direct and legacy-envelope forms therefore collide within the same declared backend scope, and dispatch/job correlation does not change the key.
