# Temporal queues and workflows

Import the optional `github.com/goforj/queue/driver/temporalqueue` module. The root queue module does not depend on the Temporal SDK. This integration uses SDK 1.45.0 and keeps the repository's Go 1.27 minimum.

Temporal adds a workflow execution for every ordinary job. Choose it when durable orchestration, native workflows and execution history justify that overhead. Redis, SQL and broker drivers remain useful for high-volume independent jobs.

## Construct and register

```go
q, err := temporalqueue.NewWithConfig(temporalqueue.Config{
    DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: "reports"},
    ClientOptions: client.Options{HostPort: "localhost:7233"},
    DefaultActivityTimeout: 30 * time.Second,
    ShutdownTimeout: 15 * time.Second,
    Register: func(registry worker.Registry) error {
        registry.RegisterWorkflow(BuildReport)
        registry.RegisterActivity(LoadReport)
        return nil
    },
}, queue.WithWorkers(8))
if err != nil { return err }
q.Register("reports:notify", notifyReport)
return q.Run(ctx)
```

Construction creates a lazy client and no pollers. Register all native workflows and activities before starting workers. Registration runs once, before any role starts polling. Framework names are reserved, dynamic registrations are unsupported, and duplicate registrations reject even when SDK duplicate checks are disabled. Every worker polling the same physical role must agree on the logical resource identity, routing, catalog, code and serialization policy. `RegistrationFingerprint` becomes available after startup; it identifies catalog names, not code compatibility.

The registration callback may register independent types concurrently, but must join that work before returning. The completed catalog is sealed for startup and replay.

Interceptors observing framework-owned workflows must preserve the SDK command semantics: activity scheduling returns immediately, and private journal publication must not yield, suppress, replace, reorder or add commands. Those operations commit together in one workflow task. Native application workflows retain their own interceptor behavior. As with SDK workflow code, adding waits or commands requires deliberate replay compatibility.

`Run` starts workers, returns worker fatal errors, and joins shutdown with a fresh bounded context. `StartWorkers` supports separately managed hosts. Such hosts must propagate worker errors and join shutdown before releasing injected dependencies. Synchronous native query, signal-dispatch and update-validation callbacks participate in shutdown drain. A timed-out shutdown means callbacks or SDK work can still be active. Retain dependencies and call `Shutdown` again to join actual completion.

## Portable work

```go
result, err := q.Dispatch(queue.NewJob("reports:notify").
    Payload([]byte(`{"report_id":42}`)).Retry(2))
// result.DispatchID identifies this submission, including an ambiguous start.

id, err := queue.Continue(q.Batch(
    queue.NewJob("reports:notify").Payload([]byte(`{"report_id":42}`)),
    queue.NewJob("reports:notify").Payload([]byte(`{"report_id":43}`)),
).Name("daily reports")).
    OnProgress(queue.NewJob("reports:progress")).
    OnFailure(queue.NewJob("reports:alert")).
    OnCompletion(queue.NewJob("reports:complete")).Dispatch(ctx)
```

Named hooks are normal registered jobs. `Message.Continuation()` provides the stable invocation ID, declaration, phase and immutable safe trigger counters. Use the invocation ID as an application idempotency key. Retries preserve it. Hook payload bytes remain the original hook payload, without business payloads or private handler errors being appended.

Business members continue while one serialized eligible hook head runs. Progress follows settlement order; ties already ready in one activation follow input order. Declaration order applies within each phase. The first failed batch member commits the failure aggregate and its completion hook, while already admitted members drain and update counters. `AllowFailures` waits for all members and permits successful aggregate completion. Exhausting a hook does not rewrite business state or prevent later hooks.

An SDK scheduling-preflight or journal-publication failure interrupts the coordinator without rewriting committed business results. Unfulfilled hooks remain observable as interrupted, without a false scheduling timestamp. Adapter availability retains its retryable error category, but the portable coordinator has no execution-level retry policy; repairing an adapter does not automatically restart a failed coordinator.

```go
state, err := q.FindBatch(ctx, id)
// Reads use original history and require no polling worker.
outcomes, err := q.FindContinuations(ctx, id)
// Only triggered obligations appear; unused definitions are absent.
```

The reader pins one run and history watermark. It validates the accepted definition, ordered committed deltas, member and hook associations, scheduling commits and safe counter transitions. Permission, decoding, unavailable service and protocol errors do not become not-found. Confirmed absent or expired history maps to `queue.ErrWorkflowNotFound`. Reads return authorized original payloads, so apply application authorization before exposing them over HTTP.

## Policies and boundaries

| Surface | Behavior |
| --- | --- |
| Default attempts | One total attempt |
| `Retry(n)` | `n+1` total attempts, with checked SDK integer limits |
| Timeout | Required resource default, overridden by positive job timeout |
| Delay, timeout, backoff | Positive durations round upward to whole seconds |
| Backoff | Fixed interval, default one second |
| Payload | Exact canonical queue bytes, up to 128 KiB per job |
| Portable members | At most 256 |
| Named definitions | At most 32, with bounded initial input and reserved journal/history footprint |
| Unique delivery | Rejected before acceptance |
| Closure callbacks | Rejected before Temporal acceptance; use named hooks |
| Unknown queue target | Rejected before acceptance |
| Store, clock, legacy envelope, resource ID overrides | Rejected; Temporal owns these policies |
| Pause, resume, queue administration, pruning | Unsupported; namespace retention remains operator-owned |
| Portable reset, retry, cron, child start, continue-as-new | Rejected at workflow and activity boundaries |
| Native workflows | SDK policies and result types remain native |

Generic queue permanent errors and panics become sanitized nonretryable failures. Retryable queue errors use the explicit attempt policy. Queue's uncommitted infrastructure outcome is unsupported on this transport and fails explicitly. Native activity results, error details, heartbeat recovery and retry policies remain SDK-native subject to the configured protection policy.

A start response can be lost after the service accepts work. Internal retries reuse one identity and input; already-started responses require matching original history. `ErrAcceptanceUnknown` means acceptance could not be established. Save the returned ID and inspect retained history. Calling public `Dispatch` again creates a new submission. Do not resubmit an unresolved old execution after retention/deletion removes its evidence. Business idempotency remains necessary.

## Native access and worker roles

```go
sdk, err := temporalqueue.Client(q)
// sdk is the resource's actual client. Leave Close to its owner.
run, err := sdk.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
    ID: "report-42", TaskQueue: "reports",
}, BuildReport, 42)
var result Report
err = run.Get(ctx, &result)
```

Native workflows can use signals, queries, validated updates, update-with-start, timers, children, version markers, continue-as-new and worker deployment routing. Persist explicit activity routing in native workflow input when deploying split roles.

Payload-level SDK activity objects receive the same callback-drain and encoded input/output bounds as typed activities. Typed activities and activity interceptors returning preencoded SDK payloads also retain the encoded result bounds, even when the SDK bypasses conversion. Their handlers own decoding and protection of raw payloads, as in native SDK usage. Registration distinguishes the SDK's complete payload-level interface from a struct with an ordinary `Execute` method; struct method prefixes and duplicate checks remain SDK-compatible.

Propagated context is checked before query handlers, update validation and update execution. New updates reject invalid context before acceptance. Driver-owned propagation, interceptor initialization or contextual data/failure converter-factory rejection while replaying an accepted workflow or update aborts workflow-task execution rather than inventing a different outcome. New workflow inputs retain terminal deterministic rejection and bounded local adapter recovery. Factories invoked for native workflow-task setup follow the SDK task-failure policy, including on the first task. Workflow-side activity factory copies retain workflow ownership; activity-worker and public-client conversions retain their separate rejection behavior. Ordinary native converter decoding errors retain SDK semantics. Bounded local adapter retries do not guarantee recovery of an existing native execution when its input cannot decode; keep retained keys and decoder dependencies available. The configured native workflow panic policy remains in effect: the default blocks for adapter recovery; explicit `FailWorkflow` fails closed. Malformed signal context is discarded before application delivery, like corrupt SDK signal arguments. A signal RPC acknowledges service acceptance, not application delivery. Temporary signal-adapter unavailability instead fails the workflow task so the original signal can replay after recovery; do not resubmit it merely because the adapter is unavailable.

The default topology combines workflow and activity polling on one task queue. Set `WorkflowTaskQueue` and `ActivityTaskQueue` together to use separate role workers. Use their corresponding worker options instead of combined `WorkerOptions`. `WorkerRouting(q)` exposes resolved roles. One remote activity task queue is supported per resource. `WithWorkers` caps shared native and portable remote activity execution on that role; local activities and Nexus have separate SDK capacities. Custom tuners, sessions, plugins, dynamic registrations and poller-behavior overrides are rejected until their capacity and lifecycle contracts are supported.

A borrowed `ClientBinding` supplies the authoritative namespace, converter and transport policy. Create it with `NewClientBinding`, pass `Config.Binding`, then close queues before closing the binding. Do not combine borrowed bindings with separate client options. Native callers borrowing `Client(q)` must finish before owner cleanup. Separate client-only resources can outlive Jobs workers for HTTP use.

## Security and operations

For mTLS, configure explicit verified TLS and supply either `TLS.Certificates` or SDK `client.NewMTLSCredentials`. Certificate material and client policy slices are snapshotted; dynamic certificate and ECH verification callbacks participate in resource drain. A nonnil empty certificate may decline optional client authentication, following Go TLS semantics.

Use namespace RBAC granting workflow start, history reads and the required worker task permissions. History reads are necessary for acceptance reconciliation and activity association as well as public state reads. Withholding history permission leaves ambiguous starts unresolved and prevents safe portable execution.

Default diagnostics redact SDK message text, payload stringification, failure messages, stacks and arbitrary details. Error type names remain visible for native retry matching. Provide a protected converter and `NewProtectedFailureConverter` when native details and heartbeat checkpoints must be retained confidentially. Custom converters must implement `BoundedDataConverter`, bound internal expansion/work, declare encoded overhead and preserve their contract in contextual copies. `ProtectedDataConverter` additionally validates authenticated envelopes. Missing keys must return the explicit adapter availability category; authentication failures must fail closed. Keep old decoding keys for retained history and replay.

Context propagators must declare bounded headers. Multiple propagators retain SDK order and last-writer behavior; their merged outgoing headers must fit the shared count and byte limits before any request or command receives them. Outer guards limit counts, encoded bytes, recursive failures, history pages and transport sizes before invoking adapters. TLS, credentials, metrics and converters participate in actual callback drain. Interceptor factories and initialization, activities, synchronous queries, signal dispatch and update validation are also drained. TLS key logging, unverified TLS, plaintext credentials, opaque transport overrides and unbounded external payload storage are rejected.

Native workflow and update bodies can remain suspended in the SDK's process-wide sticky cache after worker shutdown. Their deferred interceptor cleanup, including tracing span finalization, can run later when the SDK evicts a coroutine. Keep tracer/exporter and other cleanup dependencies alive for that SDK cache lifetime; successful queue shutdown does not join those suspended bodies or their defers. The driver does not purge the global cache, which may belong to other resources. Use activities for resource-owned application I/O.

Worker system-information providers also participate in callback drain. Provider errors and panics become sanitized unavailable samples, including during SDK startup rollback and shutdown.

Queue journal deltas contain safe counters and immutable specification references, never hook payload bytes. Original business and hook input remains in execution history and needs an approved encrypted converter if sensitive. Temporal visibility Memo contains the latest safe delta. Native application Memo and search attributes remain the application's responsibility.

Use `NewWorkflowReplayer(q)` with the same namespace/converter/catalog to replay retained captures. Protocol evolution also requires semantic comparison of regenerated journal deltas: SDK command matching alone does not compare Memo contents. The checked-in production fixture includes a same-command-count negative control.

Service replay enforces the same 64 MiB and 100,000-event bounds as retained captures, plus 1,000 pages and progressing pagination tokens. As with authoritative state reads, it requires decoded history pages and rejects opaque service `RawHistory` blobs before SDK interpretation.

Deploy compatible workers before producers using the new protocol. Keep old handlers, registrations, codecs and version routing until all open executions and retained replay obligations have drained. Portable workflow names and journal schema are versioned. A worker rollback must retain support for already accepted versions; changing the queue driver does not migrate open Temporal executions. Use a new resource/queue when changing transport and leave the old owner draining. Release scripts publish optional modules separately; a root tag alone does not publish this driver.

Continuation scheduling preserves Memo-first command order for existing histories. New histories use SDK change ID `goforj.queue.continuation.schedule-command`, version 1, to schedule and journal atomically after SDK preflight succeeds. Business scheduling uses `goforj.queue.business.schedule-preflight`, version 1, to distinguish unscheduled infrastructure failures from actual activity outcomes. Rollback workers must retain both branches while executions or replay obligations for those versions remain.

See [named continuation storage](continuations.md) for built-in store recovery and schema rollout.


For exact production scenarios, replay controls, composition evidence and release checks, see [Temporal acceptance](temporal-acceptance.md).


For local development, start the same pinned service used by acceptance tests:

```sh
docker run --rm --name queue-temporal-dev -p 127.0.0.1:7233:7233 \
  temporalio/temporal@sha256:2c344b4a39b4489fc6944db095f628f0c30659836faf11780a4dc435599e80e3 \
  server start-dev --ip 0.0.0.0 --headless
```

This development instance has ephemeral history and no namespace authentication. Production clients and workers need their configured Temporal endpoint, TLS trust and namespace permissions. Approved KMS/codec adapters additionally need authenticated outbound key-service access, bounded deadlines and retained decoding keys.

Static TLS and client policy are copied at binding construction. Use bounded certificate-selection and dynamic credential callbacks for live rotation, or roll to a new binding, move compatible producers/workers and drain the old owner before closing it. Updating the original static configuration does not rotate an existing binding.

The deployment owner manages namespace authentication/authorization, retention, archival, backups, legal holds and deletion. Preserve keys for retained histories and backups under that policy. Authoritative audit records for starts, signals, updates, cancellation, termination and namespace administration come from the secured service boundary, with owner-defined retention, integrity and export controls. Queue observers and application logs provide operational telemetry. A Web payload-codec service requires its own authenticated and authorized decryption boundary; the driver does not publish one.
