# Queue Unification Plan

Status: Active

Last updated: 2026-07-19

Baseline: `origin/main` at `18a7647`

Working branch: `refactor/unify-queue-workflow`

## Goal

Make `queue` a coherent, dependable queue and workflow library with one normal application model, explicit ownership between delivery and orchestration, truthful backend guarantees, compatibility-conscious evolution, and validation that covers every module and supported deployment shape.

This is the living execution plan. Keep it current as work lands. A task is complete only when its acceptance criteria and applicable validation pass.

## Working Agreement

- Work from the highest-priority unblocked item in the current milestone.
- Add a regression test before or with every correctness fix.
- Exercise changes through the public `*queue.Queue` path, not only private runtimes or driver internals.
- Preserve source/API, configuration, persisted-data, runtime, operational, and minimum-Go-version compatibility by default.
- Record any necessary incompatibility in the decision log before implementation, including why compatibility cannot be preserved and how users migrate.
- Treat generators, GoDoc examples, and templates as authoritative; regenerate checked-in documentation and verify a second generation produces no diff.
- Validate every affected Go module independently. Workspace success alone is insufficient.
- Preserve intentional sibling `replace` directives used for repository testing.
- Use `/tmp` for all test renders and generated application compositions.
- Keep this file focused on decisions, executable work, evidence, and remaining risk. Move lengthy design specifications into dedicated documents and link them here.

## North-Star Model

The intended architecture has four explicit layers:

1. **Application facade** — one public `*queue.Queue`, one canonical `Job`, one handler `Message`, and workflow builders.
2. **Workflow engine** — chain/batch state transitions, correlation, continuation scheduling, and durable workflow policy.
3. **Worker runtime** — handler registration, execution, concurrency, retry coordination, draining, and lifecycle state.
4. **Driver SPI** — enqueue, delivery metadata, acknowledgement or settlement, backend resources, and explicit capabilities.

Cross-cutting administration and observability may span layers, but their events and capabilities must identify which layer produced them.

The normal application path remains `*queue.Queue`. Advanced packages may expose extension points, but they must not create a second contradictory application model.

## Unified Public Surface

The following direction is accepted and governs implementation order:

- `queue` owns the canonical public `Queue`, `Job`, `Message`, handler, middleware, workflow builder/state, event, observer, store, and capability types.
- `Queue.Dispatch`, `Queue.Chain`, and `Queue.Batch` compose the same `Job` and handler model. A workflow is not a second dispatch runtime.
- Ordinary jobs remain ordinary queue jobs. They are not wrapped in a workflow envelope merely to pass through the public facade.
- There is one `queue.Observer` receiving one extensible `queue.Event` model for dispatch, enqueue, attempt, queue-control, chain, batch, and continuation facts.
- Observation is best-effort telemetry and never controls retries, workflow transitions, or business continuations.
- Reliable workflow continuations are named `Job` values persisted and dispatched through the queue. Function callbacks may remain only as explicitly ephemeral compatibility helpers.
- `bus` becomes a deprecated compatibility facade over the canonical queue model. Compatibility declarations may remain while callers migrate, but the package must not retain a second orchestration engine, store implementation, event producer, or lifecycle owner. Its independent fake remains explicit debt under M5-07.
- Internal delivery and workflow components may remain separate, but that separation is an implementation detail with explicit outcome contracts rather than duplicated application APIs.

The target application experience is intentionally small:

```go
q, err := queue.NewWorkerpool(
	queue.WithWorkers(4),
	queue.WithObserver(observer),
	queue.WithStore(store),
)
if err != nil {
	return err
}

q.Register("reports:build", buildReport)
q.Register("reports:publish", publishReport)
q.Register("reports:failed", recordFailure)

_, err = q.Dispatch(queue.NewJob("reports:build").Payload(payload))
if err != nil {
	return err
}

_, err = q.Chain(
	queue.NewJob("reports:build").Payload(payload),
	queue.NewJob("reports:publish"),
).
	OnFailure(queue.NewJob("reports:failed")).
	Dispatch(ctx)
return err
```

`OnFailure` illustrates the target durable continuation API and does not exist yet. Existing closure-based `Catch`, `Then`, `Progress`, and `Finally` methods remain compatibility surfaces until the durable replacements are implemented and documented.

The canonical event model remains flat and easy to log. It carries the union of useful transport and workflow correlation fields without nested type hierarchies:

```go
type Observer interface {
	Observe(context.Context, Event)
}

type Event struct {
	SchemaVersion int
	EventID       string
	Layer         EventLayer
	Kind          EventKind
	Driver        Driver
	Queue         string
	DispatchID    string
	JobID         string
	ChainID       string
	BatchID       string
	JobType       string
	Attempt       int
	MaxRetry      int
	Scheduled     bool
	Duration      time.Duration
	Time          time.Time
	Err           error
}
```

Exact field evolution remains compatibility-sensitive. Existing fields and event string values should be retained or adapted during migration where doing so does not preserve contradictory semantics.

## Compatibility Guardrails

- Preserve the root `Queue`, `Job`, `Message`, handler, and builder APIs wherever viable.
- Prefer forwarding aliases and deprecation periods over immediate removal of public `bus` APIs.
- Do not silently change the meaning of an existing option. Where semantics are currently broken or inconsistent, document the corrected contract and add focused compatibility tests.
- Version internal transport/workflow envelopes before changing their persisted or wire representation.
- Define mixed-version producer/worker behavior for every envelope change.
- Define forward and rollback behavior for SQL schema changes before applying them.
- Validate root and optional driver module versions independently before release.
- Do not raise the minimum Go version unless an implementation or required dependency demands it; record the exact constraint here.

## Baseline Findings

The 2026-07-18 audit established the following starting point:

- [x] Inventory all 12 Go modules.
- [x] Root unit tests pass.
- [x] Root race tests pass.
- [x] Root `go vet` fails because mutex-bearing runtime values are copied.
- [x] NATS, SQS, and RabbitMQ test packages fail to compile after the observer signature change.
- [x] The examples module does not validate independently with `GOWORK=off` because dependency sums are incomplete.
- [x] CI root tests do not cover nested driver modules.
- [x] Public-path reproductions confirm incorrect workflow retry state and broken `UniqueFor` behavior.
- [x] Driver, workflow, lifecycle, observability, capability, fake, and documentation contracts have been mapped.

The audit found release-critical semantic risks. Until the relevant milestones complete, do not make stronger durability, uniqueness, workflow recovery, or cross-backend equivalence claims.

## Decision Gates

Resolve each decision before implementing the dependent architectural milestone. Record the chosen option and rationale in the decision log.

### D-001: Retry Ownership

Status: Accepted

Required before: M1 workflow retry implementation and M2 SPI design

Decision: the worker runtime coordinates retries, drivers commit settlement or retry scheduling, and workflows transition only from committed attempt outcomes. Public handlers continue to return `error`; internal adapters classify retryable, permanent, and exhausted outcomes. An attempted retry is not a workflow fact until the responsible driver confirms it was scheduled.

The shared internal classifier uses zero-based attempt numbers and four decisions:

- success commits application success;
- retry preserves nonterminal workflow state and consumes the next application attempt only after the settlement owner schedules or begins it;
- failure commits a permanent or exhausted application outcome;
- redelivery means workflow/infrastructure state did not commit and must retry the same attempt without consuming business retry budget.

Core NATS cannot fully satisfy the durable committed-outcome contract while it uses ephemeral pub/sub. Publish plus flush may prove only an ephemeral republish; D-004 remains a required reliability decision rather than weakening the contract for durable drivers.

Asynq v0.26 checks retry exhaustion before consulting its `IsFailure` predicate. The behavior is an upstream ordering regression: revocation, skip-retry, and non-failure classification should precede exhaustion. New explicit-retry Redis tasks reserve one Asynq transport slot and carry their application retry budget in a task header; workers classify against the application budget, explicitly archive its terminal outcome, and reuse the reserve for uncommitted or lease-recovery redelivery without incrementing the application attempt. This preserves one Asynq settlement owner and queued-task decoding, but requires workers to roll out before producers and cannot repair an already-exhausted legacy task. An upstream fix remains preferred so the compatibility reserve can eventually be removed.

### D-002: Workflow Durability Contract

Status: Open

Required before: M3

Decide whether distributed backends:

- require an explicitly durable workflow store;
- automatically derive a compatible store where possible; or
- permit memory workflows only with a clear ephemeral-mode diagnostic.

Preferred direction: allow memory mode for local development and explicitly ephemeral workflows, but require or loudly diagnose a durable store when durable workflows run across processes.

### D-003: Public `bus` Compatibility

Status: Accepted

Required before: M2 public API consolidation

Decision: make `*queue.Queue` the sole documented application facade. Retain `bus` temporarily as a deprecated forwarding compatibility package, then remove its independent implementation only after root equivalents and migration documentation exist.

### D-004: NATS Product Contract

Status: Open

Required before: claiming queue durability for NATS

Choose between:

- replacing Core NATS pub/sub with JetStream durable work-queue semantics; or
- retaining the existing adapter under explicitly ephemeral pub/sub semantics and naming.

Preferred direction: use JetStream durable consumers and queue-group delivery for the queue driver.

### D-005: Queue Targeting

Status: Open

Required before: M1 default-queue corrections

Define whether one runtime consumes:

- only one configured queue;
- every queue targeted through it; or
- an explicit configured queue set with weights/concurrency.

Preferred direction: distinguish producer target selection from worker subscription configuration, apply `DefaultQueue` centrally to empty targets, and reject dispatches that the configured consumer model cannot service only when that is an explicit contract.

### D-006: Observer and Event Model

Status: Accepted

Required before: M2 public model consolidation

Decision: use one root `queue.Observer` and one root `queue.Event` superset. `queue.WithObserver` observes both delivery and workflow facts. Existing root `WorkflowObserver`, `WorkflowObserverFunc`, `WorkflowEvent`, and `WorkflowEventKind` names become deprecated aliases or adapters. The compatibility `bus` package translates to its legacy event representation for a deprecation period instead of retaining a second event producer.

### D-007: Workflow Continuations

Status: Accepted

Required before: M3 callback durability work

Decision: observers are never workflow callbacks. Durable success, failure, progress, and completion continuations are named queue jobs persisted with workflow state. Existing function callbacks are explicitly ephemeral and remain only for compatibility and local convenience until a future compatibility boundary permits removal.

### D-008: Direct Job Execution

Status: Accepted

Required before: M2 runtime consolidation

Decision: direct `Queue.Dispatch` does not create a workflow envelope. Chains and batches attach private workflow correlation to the same canonical job/delivery model, and the worker runtime reports committed outcomes back to the workflow engine.

### D-009: Observer Compatibility Boundary

Status: Accepted

Required before: publishing the unified observer release

Decision: the observer collapse is an intentional source/API and runtime-behavior compatibility boundary in the next pre-v1 feature release. `WithObserver` accepts the canonical root `Observer`, legacy root workflow names alias that root model, and one observer receives all layers. Preserving the exact old type identity would require retaining the second public event contract or accepting an untyped option, both of which conflict with the requested collapse and reliable compile-time contracts.

Migration requirements:

- replace unkeyed `queue.Event` and `bus.Event` literals with keyed literals because the event envelopes gained correlation fields;
- adapt custom `bus.Observer` implementations with `queue.ObserverFunc`, or keep raw-runtime legacy bus consumers on `bus.WithObserver`; an existing `*queue.Queue` must receive its observer at root construction;
- filter `Event.Layer` when an existing workflow observer should retain workflow-only volume;
- make observer-owned mutable state concurrency-safe because dispatchers and workers may call the same observer concurrently.

This boundary does not itself change configuration files, persisted workflow data, wire envelopes, operational rollout, or the minimum Go version.

### D-010: Public Ownership and Dependency Direction

Status: Accepted

Required before: M2 public API consolidation

Decision: root `queue` owns the canonical application model. The orchestration implementation moves behind `internal/workflow`, root composes that internal engine directly, and public `bus` becomes a forwarding compatibility package. Do not create another public workflow-model package. Preserve the legacy raw-`busruntime.Runtime` construction path temporarily while normal `bus.New(*queue.Queue)` calls route to the root facade.

The migration is staged to avoid an import cycle and an all-at-once source break:

1. extract the existing bus engine behind `internal/workflow` without changing event names, JSON envelopes, SQL schemas, stores, retry behavior, or public type identity;
2. switch root production code from public `bus` imports to the internal engine and add an import-direction guard;
3. define canonical root `Message`, dispatch/state, middleware, store, and builder contracts one model at a time;
4. turn compatible `bus` declarations into deprecated aliases/adapters, retaining legacy composite-literal fields until a separately approved compatibility boundary;
5. route option-free `bus.New(*queue.Queue)` through root and reject construction-only options explicitly instead of constructing another independently configured engine.

The extraction itself is source/API, configuration, persisted-data, runtime-behavior, operational, wire, and minimum-Go-version neutral. The facade conversion deliberately changes runtime behavior for every `bus.New(*queue.Queue)`: compatibility views now share the root handler registry, store, observer, middleware, and lifecycle instead of constructing independent state over the same physical runtime. Code that needs isolation must use distinct runtimes. It also changes configuration and runtime behavior for option-bearing `bus.New(*queue.Queue, ...)` and `bus.NewWithStore(*queue.Queue, ...)`: those calls now return `bus.ErrQueueOptionsUnsupported` because options cannot apply only to a shared view. Callers migrate those options to root queue construction and then use option-free `bus.New(existingQueue)`; the raw-`busruntime.Runtime` route retains its legacy options. Later alias conversions require focused source-compatibility fixtures before landing.

## Milestone M0: Restore a Trustworthy Baseline

Objective: every module builds and its non-network validation runs independently, while CI exposes rather than hides module drift.

- [x] **M0-01 — Repair nested driver test compilation.** Update NATS, SQS, and RabbitMQ tests to the current observer contract and verify the existing all-module compile guard covers every optional driver module.
- [x] **M0-02 — Make examples independently reproducible.** Repair the examples module sums and validate it with `GOWORK=off`.
- [x] **M0-03 — Eliminate mutex-copy hazards.** Replace runtime value cloning with shared lifecycle state and verify context-bound handles retain synchronized worker state.
- [x] **M0-04 — Expand CI across module boundaries.** Run unit tests for every module, root race tests, vet, README snippet checks, generated example checks, and independent `GOWORK=off` passes where applicable.
- [x] **M0-05 — Fix known stale documentation sources.** Correct the batch callback example, Go-version badge, and sync lifecycle examples in authoritative sources, then regenerate documentation.
- [x] **M0-06 — Establish public-path contract fixtures.** Ensure shared fixtures construct and exercise public `*queue.Queue` values rather than private runtime adapters.
- [x] **M0-07 — Add a module/version inventory guard.** Check `go.mod` files, workspace membership, sibling replacements, module tags, and release-script coverage.

Exit criteria:

- Every module's non-network unit suite compiles and passes independently.
- Root unit, race, and vet passes are green.
- CI cannot pass while an optional driver test package fails to compile.
- Generated documentation is reproducible with no second-run diff.

## Milestone M1: Correct Existing Public Semantics

Objective: repair behavior already promised by the public API without first requiring a broad redesign.

### Retry and terminal outcomes

- [ ] **M1-01 — Add retry-state regressions.** Cover chain and batch transient success, terminal exhaustion, fatal errors, attempt numbers, callbacks, and event ordering through public queues. Public transient, exhaustion, permanent, attempt, callback, ordering, downstream-node, allowed-batch-failure ordering, and workflow-store fault cases are in place. Real SQLite proves a surviving same-attempt row can use an exact transition receipt to recover terminal-chain, completed-predecessor, batch-member, and aggregate-completion facts after forced finalization failure without executing application code twice. Active predecessor recovery now re-dispatches the immediate successor after definite enqueue rejection without replaying the predecessor; the same liveness-only behavior covers a missing receipt, a decorated store without receipt capability, and supported receipts with different or legacy generation provenance. Those weaker paths emit no predecessor facts or callbacks, and progressed or terminal state is a no-op. The remaining already-enqueued-but-not-progressed ambiguity makes continuation at-least-once, not exactly-once. Focused built-in contracts prove a receipt-backed terminal chain failure returns its first persisted permanent cause across exact, different, or legacy recovered-generation provenance without repeating the handler, callbacks, or logical failure facts; invalid receipts fail closed, while receipt-absent legacy failure rows retain weaker replay. A real SQLite archive-failure fixture extends that proof across multiple recovery finalization faults, preserves the cause and receipt lineage, and reaches `dead` with one application/workflow occurrence. The two-member terminal-owner case proves the completing member's receipt recovers `BatchCompleted` without crediting the earlier member. Repeated SQLite success and failed-batch finalization faults prove the same fenced best-effort lineage repair; failed batch recovery uses a generic permanent cause and reaches SQL's `dead` archive without fabricating the unpersisted application error. A separate race proves application retry clears the earlier generation link, while the later attempt can own a new receipt and recover a subsequent finalization failure of that same attempt. Real MySQL and PostgreSQL successful terminal-chain scenarios prove the versioned receipt-to-generation contract, and twelve-worker fail-fast races on both dialects prove one serialized aggregate terminal owner. No-redelivery publication and remaining settlement-owner `WithStore` gates remain.
- [x] **M1-02 — Stop premature workflow failure.** Chain and batch state, callbacks, and terminal logical events now wait for a permanent or exhausted outcome; retryable attempts remain worker-layer failures.
- [x] **M1-03 — Make `FailOnError` operational.** `queue.Permanent`, `FailOnError`, and the shared classifier now stop application retries across local, Redis, SQL, NATS, SQS, and RabbitMQ paths.
- [ ] **M1-04 — Emit only committed outcome events.** Workflow mutation failures are classified as uncommitted before terminal facts publish, retry facts appear only when a numbered retry delivery begins, and generic/archive predictions were removed. SQL, SQS, and RabbitMQ defer positive process/workflow facts until durable row finalization, deletion, or acknowledgement; failed settlement emits a correlated worker fact. SQL claims carry opaque generation provenance, while direct built-in workflow stores separately record exact transition ownership plus receipt and observer-event schema versions. The observer schema is independent from the workflow-envelope protocol, and an unsupported receipt or event version fails recovery closed with an uncommitted error before acknowledgement, application execution, state-commit signaling, or fact publication. A supported identity-matching receipt suppresses duplicate application execution; exact recovered-generation ownership additionally gates republishing successful chain-node, batch-member, and aggregate facts with deterministic IDs after the new settlement. A failed chain receipt returns the authoritative persisted permanent cause across generation variants without repeating occurrence-based failure facts or callbacks. Failed batch recovery returns a generic permanent cause because its original error is not persisted; both failure paths archive without emitting replacement member facts. Real SQLite, MySQL, and PostgreSQL finalization-failure tests cover the supported successful terminal-chain path; focused memory/SQLite contracts plus a real repeated SQLite archive-failure fixture cover atomic failed-chain receipts; repeated SQLite finalization faults cover best-effort lineage repair; and SQLite plus real MySQL/PostgreSQL concurrency gates prove aggregate completion/terminal effects belong to one serialized receipt owner rather than aggregate state or an earlier member. When a generation claims a receipt-backed transition but later workflow infrastructure still needs same-attempt redelivery, the delivery-settlement application-state signal makes SQL retain that current generation instead of inherited provenance; focused signal, token-selection, and chain post-transition tests cover the handoff. Queue provenance, aggregate state, and response-local `claimedNow` are not durable proof; application retry clears the earlier generation link. Callback redelivery no longer turns an at-most-once marker into false success, premature callback envelopes cannot consume the marker, callback panics become failures, and new Redis tasks preserve final-attempt uncommitted redelivery through one header-marked transport reserve. Custom/decorated/raw stores have weaker private-capability guarantees. A driver-owned settlement outbox is still required when finalization applied but its deferred observer calls were lost; durable callback/continuation intents, physical commit/readback ambiguity, cross-driver provenance, Redis post-`Done` success, legacy Redis task rollout, and NATS's explicit nonconformance also remain.

### Identity and uniqueness

- [x] **M1-05 — Define logical job identity.** The versioned identity length-frames effective queue, logical type, and canonical payload while excluding volatile correlation IDs and delivery options. Golden vectors pin direct/workflow parity and normalize absent, zero-byte, and exact JSON `null` payloads before direct execution replaces legacy envelopes.
- [x] **M1-06 — Repair public `UniqueFor`.** Public workflows use canonical logical identity across every backend; concurrent public SQL/Redis clients prove one backend-wide winner, restart persistence, and documented instance-versus-backend scope.
- [ ] **M1-07 — Make uniqueness acquisition atomic with acceptance.** SQL claims and queue rows now share one transaction; local and broker paths compensate known pre-acceptance failures with token-owned claims. Redis compensates only definite physical duplicates and intentionally retains other ambiguous enqueue outcomes, but its separate claim/enqueue operations leave an unavoidable crash window until a stronger atomic protocol exists.

### Configuration and lifecycle behavior

- [ ] **M1-08 — Apply `DefaultQueue` centrally.** Preserve explicit queue names and ensure empty names follow one documented rule across every driver.
- [ ] **M1-09 — Make worker targeting explicit.** Implement D-005 and test that every accepted target is consumed by the intended runtime configuration.
- [ ] **M1-10 — Make `WithWorkers` effective.** Workerpool now applies the configured count to execution concurrency and derives its default buffer from that count. Verify the same end-to-end behavior for SQL, Redis, NATS, SQS, and RabbitMQ rather than wrapper state; Core NATS plain subscriptions currently make higher counts duplicate broadcast consumers rather than queue workers.
- [ ] **M1-11 — Clarify sync startup semantics.** Either register bus handlers immediately for synchronous dispatch or consistently require startup and correct every example and contract.
- [ ] **M1-12 — Validate registrations and options.** Nil handler registration is now a consistent no-op across root, workflow-engine, and deprecated bus boundaries, including after a valid registration. Reject nil options deterministically; preserve explicit zero versus unset retry, timeout, and backoff values.
- [ ] **M1-13 — Normalize payload contracts.** Give `Payload` and `PayloadJSON` distinct, documented behavior and consistent binding errors.

### Readiness, capabilities, and shutdown

- [ ] **M1-14 — Restore backend readiness.** Forward readiness through every bridge and add negative unreachable-backend tests.
- [ ] **M1-15 — Replace wrapper-inflated capability checks.** Report actual capabilities independently of observers and adapters.
- [ ] **M1-16 — Make shutdown retryable and context-aware.** Root native/external lifecycles serialize startup with shutdown, latch drain intent before waiting on startup, retain partially started workers for cleanup or retry, keep one replaceable handler slot per backend registration, lease dispatch/readiness/control/admin operations before resources can close, retain state after failed cleanup, reject restart while draining, and close successfully exactly once. Redis owned resources close at most once, report joined close diagnostics to the caller that performs cleanup, and let a later root shutdown converge to terminal success instead of remaining permanently draining. Scoped continuation permits expire when their handler returns, while Sync and Workerpool reserve accepted delayed descendants and drain them within the caller's shutdown deadline. Context-unaware broker dialing and RabbitMQ resource closure, plus real broker deadline evidence, remain.
- [x] **M1-17 — Close producer-owned resources without worker startup.** Native and external shutdown now reach producer cleanup without worker startup, Redis closes every owned producer client exactly once, SQL closes only internally opened handles, and a real SQLite test proves caller-owned handles remain usable.
- [ ] **M1-18 — Drain before broker settlement resources close.** Root operation leases prevent producer closure during admitted public work; SQS and RabbitMQ workers wait for active delivery loops before closing settlement resources; and NATS startup/drain coordination waits for admitted callbacks. Real in-flight delete, acknowledgement, replacement-publication, and connection-close shutdown scenarios remain. Core NATS can still accept a replacement after its own ephemeral subscription has drained, so D-004 remains a correctness boundary rather than a shutdown guarantee.

Exit criteria:

- The retry, uniqueness, default queue, worker count, readiness, capability, and shutdown contracts pass for every applicable driver.
- No option changes meaning during queue-to-workflow-to-driver conversion.
- Enabling an observer cannot change reported capabilities or job outcomes.

## Milestone M2: Establish Clean Internal Boundaries

Objective: introduce a stable internal architecture while preserving the root application API.

- [ ] **M2-01 — Specify the driver SPI.** Define immutable enqueue input, option presence, logical identity, delivery attempt metadata, settlement outcomes, lifecycle, and capabilities.
  - [x] Add the additive attempt-classification foundation shared by orchestration and delivery: zero-based attempt metadata, success/retry/failure/redelivery decisions, and distinct permanent versus uncommitted error markers.
  - [x] Propagate attempt metadata through every root/driver handler path before changing workflow transitions; Redis now preserves queue/attempt/retry metadata even without observers, and SQL reconstruction has a direct contract test.
  - [x] Add versioned, driver-neutral direct-delivery metadata for correlation outside application payloads; local jobs, Redis headers, broker messages, and nullable SQL storage now round-trip the same record and reject unknown versions without rejecting the job.
  - [ ] Move committed retry/archive emission to each settlement owner and define enqueue acceptance receipts.
- [ ] **M2-02 — Create a domain-neutral core/SPI package.** Drivers depend on this package rather than importing root helpers through re-export and global hook layers.
- [ ] **M2-03 — Introduce adapters alongside existing drivers.** Migrate one local and one durable driver first, keeping compatibility tests on both paths.
- [ ] **M2-04 — Remove the mutable runtime hook bridge.** Retire `any`-based global initialization only after every driver uses the new SPI.
- [x] **M2-05 — Consolidate the Job model.** Root `queue.Job` is the sole canonical public application specification, `queue.Message` is the delivered handler model, and `queue.StoredJob` is the persisted workflow model. Direct dispatch freezes exact root payload bytes instead of passing through the private fluent workflow DTO. `bus.Job` remains only as the documented source-compatible boundary DTO whose deferred JSON conversion occurs once at facade dispatch.
- [ ] **M2-06 — Version the envelope.** Define schema evolution, unknown-version behavior, mixed producer/worker deployments, and rollback.
- [x] **M2-07 — Resolve the public `bus` direction.** Implemented D-003 and D-010 in bounded slices: extracted `internal/workflow`, removed root production imports of public `bus`, established physical root models, then made `bus` a deprecated forwarding facade with source- and wire-compatibility fixtures. One internal engine now owns orchestration; root composes it directly through physical root messages, middleware, workflow records, and stores; and `bus.New(existingQueue)` wraps that exact engine without registering another. Legacy `bus.Job`, `Event`, `Observer`, `Bus`, `Option`, and self-returning builder interfaces remain physical boundary contracts, while compatible model names forward to root. Literal protocol, transport-boundary, legacy SQLite, source, construction, package-identity, import-direction, adapter, and deferred-encoding fixtures pin the migration. The full module, race, generated-documentation, local/SQLite, Redis, and NATS validation matrix passes.
- [ ] **M2-08 — Separate producer and worker lifecycle.** Model start, running, draining, stopped, and failed states without `sync.Once` poisoning.
- [x] **M2-09 — Collapse root observers.** One root observer receives delivery and workflow events through a shared sink without duplicate execution events. The legacy public `bus` observer is now translated only at the deprecated raw-runtime compatibility boundary.
- [x] **M2-10 — Stop enveloping direct jobs as workflows.** Root dispatch now sends the application type and exact payload with versioned out-of-payload correlation. One engine executor still owns middleware, handler lookup, logical events, attempt classification, and settlement deferral. Every backend round-trips supported metadata, old version-one envelopes remain readable, reserved protocol-name applications retain the legacy route, raw-runtime `bus` bytes remain frozen, and `WithLegacyDirectEnvelope` supports a safe workers-first rollout. Direct/envelope uniqueness parity remains on the existing `v1` key. The additive SQL column, asymmetric mixed-worker boundary, exact payload correction, and rollback procedure are documented in `docs/direct-delivery-migration.md`.

Exit criteria:

- Optional drivers no longer depend on mutable global runtime hooks.
- The root public API is backed by one explicit composition path.
- Mixed supported module/envelope versions have documented and tested behavior.

## Milestone M3: Make Workflow Semantics Durable and Atomic

Objective: chains, batches, callbacks, and stores behave correctly across retries, concurrency, restarts, and multiple processes.

- [ ] **M3-01 — Implement the workflow durability contract.** Apply D-002 at construction/readiness time.
- [ ] **M3-02 — Replace durable closure callbacks.** Persist named continuation jobs or registered callback identifiers; keep closures only as explicit ephemeral compatibility behavior.
- [ ] **M3-03 — Add store lifecycle ownership.** Close internally opened SQL resources and never close caller-owned resources.
- [x] **M3-04 — Make chain advancement atomic.** Memory and SQL validate immutable node order, then compare-and-swap both success and failure against the current `NextIndex`, so the first committed outcome for a node remains authoritative. Late contradictory deliveries cannot change state or publish opposite logical facts, legacy completion-then-failure rows retain completion precedence, and built-in `FailChain` preserves the first terminal cause instead of allowing a later call to rewrite authoritative recovery state. Unknown, future, duplicate-ID, and caller-aliasing branches are covered. Thirty-two-way contracts pass repeatedly on SQLite, MySQL, and PostgreSQL. Recovery re-dispatches an immediate still-pending successor without replaying the predecessor when exact receipt ownership, a missing receipt, a store without receipt capability, or different/legacy receipt provenance accompanies durable success state. Only exact ownership reconstructs predecessor facts; progressed and terminal state is a no-op. Supported receipt cancellation/completion shape is validated before liveness dispatch, corruption fails uncommitted without effects, and a rejected successor remains uncommitted for another recovery. The post-commit ambiguity where enqueue may have succeeded but workflow state has not progressed remains an M3-06 concern.
- [x] **M3-05 — Make batch aggregation atomic.** SQL conditionally claims each member and applies arithmetic aggregate updates in the same transaction, preventing both duplicate decrements and distinct-member counter overwrites. Built-in memory settlement holds one mutex, while MySQL/PostgreSQL lock the parent row after the member claim so only one false-to-true transition owns terminal aggregate effects. The additive `WorkflowOutcomeStore` reports first-writer outcome-category ownership without breaking established custom `WorkflowStore` implementations, and the runtime suppresses contradictory job/batch facts, progress, and callbacks. Concurrent mixed success/failure contracts pass on memory, SQLite, MySQL, and PostgreSQL; twelve-worker fail-fast races on each server dialect prove one aggregate receipt and one terminal failed/cancelled fact pair. Aggregate recovery now fails uncommitted when a SQL aggregate row omits completion, cancellation does not own a failed outcome, the row's incarnation is stale, or a row naming the requested logical member disagrees with that member receipt's complete owner/outcome; runtime validation also requires the aggregate flags to agree with live terminal state. `TestSQLStoreBatchAggregateOwnershipMismatchFailsClosed`, `TestSQLStoreBatchAggregateIncarnationMismatchFailsClosed`, and `TestBatchRecoveryRejectsInvalidAggregateReceiptShape` cover those branches. The established batch model does not persist per-member cause text, so restart recovery uses a generic permanent cause to archive an already-committed failed member rather than fabricating the original error.
- [ ] **M3-06 — Recover partial dispatch.** Ensure a partially enqueued batch or chain reaches a recoverable terminal or resumable state rather than permanent pending state. Validated chain success now conservatively re-dispatches the live immediate successor after definite rejection even when the receipt is missing, hidden by a custom/decorated store, or owned by different/legacy generation provenance; those weaker paths restore only liveness and do not reconstruct predecessor facts or callbacks. Recovery still cannot distinguish a missing successor from one already enqueued and not yet progressed, so persisted successor intent is required for exact recovery. Persist per-member batch failure detail if recovered callbacks and facts must reproduce the first physical cause exactly. The aggregate receipt proves which member transaction made a batch terminal, including the SQLite two-member recovery and real MySQL/PostgreSQL terminal-owner races, but it does not retain fan-out, callback, progress, or observer-publication intent. Keep those durable intents separate from the completed state-transition provenance gate.
- [ ] **M3-07 — Make callback invocation truthful.** State validation now precedes idempotency claims, missing process-local closures fail visibly, panics become callback failures, duplicate envelopes emit no orphan start, and reverse-order serialized sibling callbacks each complete once. The marker still precedes application success and callback enqueue errors have no durable recovery path, so named persisted continuations remain required.
- [ ] **M3-08 — Unify memory and SQL store contracts.** Align missing-ID, terminal transition, clock, copy/ownership, and validation behavior.
- [ ] **M3-09 — Make schema migration configurable.** `DisableAutoMigrate` is the additive queue-schema opt-out while the established queue default remains enabled; startup is retryable after real SQLite DDL lock failure, and a real no-DDL test proves externally managed queue-schema ownership. Workflow schema has a separate explicit constructor boundary: `NewSQLStore` preserves legacy migration-on-first-use behavior regardless of the false `SQLStoreConfig.AutoMigrate` zero value, while `NewSQLStoreWithManagedSchema` performs no DDL. Workflow auto-schema creates versioned `bus_workflow_transition_receipts` beside the established state tables; non-null `receipt_version` and `event_schema_version` fence durable interpretation, and unknown values fail recovery closed rather than becoming indistinguishable from absence. The event schema versions the shared observer envelope independently from the workflow-envelope protocol. Real SQLite, MySQL, and PostgreSQL recovery scenarios exercise each dialect's fresh auto-created supported-version receipt path. Managed-schema callers must precreate every column, and rollback should quiesce new workers and retain the receipt table because old binaries ignore it. A wholly fresh MySQL schema uses 255-byte workflow/member and receipt identities plus 512-byte callback keys. When only the receipt table is missing beside established state, ordinary startup derives its shared workflow width from the larger effective chain-or-batch capacity and its member width from the larger chain-node-or-batch-job capacity; the real legacy-width upgrade fixture proves the 512/512 path with identities above fresh defaults. Existing receipt tables are never altered and continue to intersect connected-schema limits. An incompatible existing table therefore needs a quiescent managed migration and a fresh store. If derived widths exceed the server's composite-key budget, startup fails with both widths and schema-first guidance rather than narrowing live tables; operators must provision a compatible indexed schema or explicitly migrate supported identity limits and existing data. Every caller-managed non-`VARBINARY` identity schema still needs the migration recorded in DL-019. Managed-schema migration/rollback, real cross-dialect pruning and physical commit/readback ambiguity, concurrent startup, and permission gates remain open, along with corresponding queue-table evidence for the uniqueness expiry index and processing-token column.
- [ ] **M3-10 — Reclassify the Temporal adapter.** Either implement a real external workflow-engine contract or clearly separate the current façade from queue-backed workflow guarantees.

Exit criteria:

- Durable workflows survive process restart and producer/worker separation.
- Duplicate deliveries cannot duplicate state transitions or callbacks.
- Store behavior is consistent and concurrency-tested on SQLite, MySQL, and PostgreSQL.

## Milestone M4: Converge Driver Guarantees

Objective: every advertised capability has a conformance test and every semantic difference is explicit.

- [ ] **M4-01 — Resolve NATS semantics.** Implement D-004 and test two workers, crash recovery, delayed work, broker restart, and poison handling.
- [ ] **M4-02 — Define poison/dead-letter behavior.** Prevent silent deletion/acknowledgement of malformed, unhandled, and terminally failed jobs in SQS and RabbitMQ.
- [ ] **M4-03 — Harden SQS delivery.** Configure visibility/redrive behavior, extend visibility for long handlers, validate credential pairs, and add deterministic receive-failure coverage. Missing receipts and delete failures now surface as settlement failures without false success.
- [ ] **M4-04 — Harden RabbitMQ delivery.** Preserve positive publisher confirms for dispatch/retry while adding worker reconnect, context-aware dialing, and real safe-settlement drain scenarios.
- [ ] **M4-05 — Harden Redis resources and admin.** Owned producer clients and state stores now close exactly once; align totals/windows, remove unreachable queue-resolution branches, and define bounded clear behavior.
- [ ] **M4-06 — Harden SQL execution and admin.** Finalization retries are bounded, require one affected row, and surface settlement failure; constrain active-job admin races, preserve timeout precision, and finish claim/recovery indexing evidence.
- [ ] **M4-07 — Make uniqueness claims precise.** Label each driver as process-local or distributed and test exactly that scope.
- [ ] **M4-08 — Separate guarantees from evidence.** Maintain a portable contract plus a driver evidence matrix whose cells link to executable scenarios.

Exit criteria:

- No driver is described as at-least-once unless acceptance, persistence, settlement, retry, and crash boundaries support that claim.
- Unsupported capabilities report false before invocation rather than failing after an optimistic support check.
- Multi-process and broker-fault scenarios cover every durable backend.

## Milestone M5: Unify Observability, Administration, and Test Doubles

Objective: operational surfaces describe real state consistently and testing APIs model production behavior.

- [x] **M5-01 — Define a shared root event envelope.** The normal facade now includes stable correlation, layer/source, queue, logical job, delivery attempt, event identity, and timestamps. Settlement-owner completion remains M1-04, and the legacy `bus` envelope survives only as a translated compatibility shape.
- [x] **M5-02 — Preserve distinct event vocabularies without duplicate observer models.** The root observer retains effective queue, logical job key/type, delivery attempt, and workflow correlation across queue, worker, aggregate, and callback facts. The legacy `bus.Event` shape is now translated only at the deprecated compatibility boundary; it no longer owns an event producer or orchestration runtime.
- [x] **M5-03 — Correct event ordering.** Local acceptance callbacks and workerpool delivery gates ensure synchronous/in-process processing cannot begin before enqueue acceptance appears to observers; distributed arrival order remains correlation-based rather than globally ordered.
- [ ] **M5-04 — Make stats semantically comparable.** Define pending, scheduled, retry, active, processed, failed, and throughput windows for each capability level.
- [ ] **M5-05 — Make history instance-scoped and truthful.** Do not present process-wide sampled memory as durable backend history.
- [ ] **M5-06 — Consolidate admin APIs.** Remove `any`-based duplicate paths over time and align not-found, unsupported, and active-operation behavior.
- [x] **M5-07 — Consolidate fakes.** `queue.NewFake` now owns one concurrency-safe direct/workflow recorder; deprecated `bus.Fake` and `queuefake.Fake` are typed compatibility views over that state. Direct dispatch shares production conversion and validation, while chain/batch builders run through the production workflow engine, record only accepted `Dispatch` calls, retain policy, and expose isolated canonical records and lookup state.
- [ ] **M5-08 — Add observer failure policy.** Define panic/error handling instead of silently swallowing observer failures without diagnostics.

Exit criteria:

- Events correspond to committed state transitions.
- Stats and admin operations have documented cross-driver meanings.
- Tests using the public fake exercise the same job conversion and validation rules as production queues.

## Milestone M6: Documentation, Compatibility, and Release Readiness

Objective: documentation and release mechanics accurately describe the implemented system.

- [ ] **M6-01 — Replace stale architecture snapshots.** Reconcile `design.md`, `bus-design.md`, and the one-path rationale; label historical proposals clearly.
- [ ] **M6-02 — Publish a delivery contract.** Define acceptance, durability, retries, duplicates, ordering, poison handling, and failure boundaries.
- [ ] **M6-03 — Publish a workflow/store contract.** Define durability modes, callback recovery, retry ownership, concurrency, pruning, and retention.
- [ ] **M6-04 — Expand compatibility policy.** Cover source API, configuration, persisted data, wire envelopes, SQL schemas, mixed module versions, operations, and minimum Go version.
- [ ] **M6-05 — Make all examples executable.** Expected output must immediately follow producing calls and generated examples must run successfully where behavior is being demonstrated.
- [ ] **M6-06 — Complete GoDoc quality pass.** Add compliant comments for exported and private entities while explaining constraints and intent rather than syntax.
- [ ] **M6-07 — Validate the largest generated composition.** Use repository-pinned versions and render outside the repository.
- [ ] **M6-08 — Exercise release scripts and module tags.** Verify every independently published module and a downstream `GOWORK=off` integration against published versions.

Exit criteria:

- Documentation contains one normal application model.
- Every public guarantee links to tests or a clearly scoped limitation.
- Every module can be released and consumed independently through its intended tag convention.

## Required Validation Matrix

Run the applicable subset continuously and the full matrix before milestone completion or release.

### Root module

```bash
GOCACHE=/tmp/gocache GOMODCACHE=/tmp/gomodcache go test ./...
GOCACHE=/tmp/gocache GOMODCACHE=/tmp/gomodcache go test -race ./...
GOCACHE=/tmp/gocache GOMODCACHE=/tmp/gomodcache go vet ./...
```

### All modules

- Root
- `docs`
- `examples`
- `integration`
- `driver/sqlqueuecore`
- `driver/mysqlqueue`
- `driver/postgresqueue`
- `driver/sqlitequeue`
- `driver/redisqueue`
- `driver/natsqueue`
- `driver/sqsqueue`
- `driver/rabbitmqqueue`

For each relevant module, run unit and vet passes independently with `GOWORK=off`. Use the repository's all-module script when it covers the required mode, and keep a direct module inventory guard so newly nested modules cannot be omitted.

### Shared semantic contracts

Each applicable backend must cover:

- public dispatch and processing;
- empty/default and explicit queue targeting;
- retry attempt metadata, backoff, fatal errors, and exhaustion;
- uniqueness at its declared scope, including a failed first dispatch;
- delayed work and restart behavior;
- malformed, unhandled, poison, and terminal failures;
- negative readiness;
- actual concurrency;
- cancellation, drain deadlines, and resource closure;
- two worker processes where distributed behavior is claimed;
- workflow recovery, duplicate delivery, and callbacks where workflows are supported.

Network-backed integration validation should use the required elevated execution path. Test renders must always be created under `/tmp`, never inside the repository.

## Definition of Done for Every Change

- The behavior and compatibility category are identified.
- New validation branches and failure modes have direct tests.
- Every affected module passes unit tests and vet independently.
- Race testing is run when concurrency or lifecycle is involved.
- Generated mirrors are regenerated from their authoritative source and a second generation is clean.
- Documentation describes the implemented behavior without overstating guarantees.
- `git status` and the staged diff contain only intended files.
- This plan's checkboxes, decision log, baseline, and progress log are updated in the same change when applicable.

## Decision Log

Record accepted decisions here using the next stable ID.

| ID | Date | Decision | Compatibility and migration notes |
| --- | --- | --- | --- |
| DL-001 | 2026-07-18 | Use this file as the living execution ledger and preserve compatibility by default. | Architectural cleanup alone does not authorize public API, configuration, persisted-data, runtime, or Go-version breaks. |
| DL-002 | 2026-07-18 | Make `queue` the only canonical application model and reduce `bus` to a deprecated forwarding facade. | Root APIs remain the migration target. Existing `bus` users receive adapters and deprecation guidance before independent implementations are removed. |
| DL-003 | 2026-07-18 | Use one root observer and event model across delivery and workflows. | Legacy workflow observer names become aliases/adapters; legacy `bus` event consumers receive translated events during migration. |
| DL-004 | 2026-07-18 | Keep observation separate from reliable workflow continuation. | Durable continuations become persisted jobs; closure callbacks are explicitly ephemeral compatibility behavior. |
| DL-005 | 2026-07-18 | Dispatch direct jobs without a workflow envelope. | Envelope/schema changes require mixed-version tests; logical job identity must remain stable for uniqueness. |
| DL-006 | 2026-07-18 | Make worker settlement the authoritative retry boundary. | Workflow state and events follow only committed retry, permanent failure, exhaustion, or success outcomes. |
| DL-007 | 2026-07-18 | Take an explicit pre-v1 observer compatibility boundary instead of retaining two typed models or using `any`. | Migration covers keyed `queue.Event` and `bus.Event` literals, custom bus observers, layer filtering, and concurrent observer calls; no persisted-data or wire change is implied. |
| DL-008 | 2026-07-18 | Invert orchestration dependencies through `internal/workflow`; root owns public models and `bus` becomes a compatibility facade. | Extract behavior first, preserve the legacy raw-runtime route, and migrate type ownership incrementally with compile and wire fixtures. |
| DL-009 | 2026-07-18 | Preserve an explicit zero workflow retry budget and require positive settlement before SQL/SQS/RabbitMQ success facts. | This corrects runtime behavior: Redis no longer substitutes Asynq's 25-retry default, NATS dispatch gains flush latency, RabbitMQ dispatch gains confirmation latency, SQS rejects an SDK success without a service `MessageId`, and observer success timing moves later. Deploy Redis workers before producers, set `.Retry(25)` explicitly if the old fallback was intentional, update SQS test doubles to return a message ID, and treat canonical uniqueness cutover as the documented operational migration. |
| DL-010 | 2026-07-18 | Preserve database migration-on-start as the default and add `DisableAutoMigrate` as the explicit externally managed-schema opt-out. | Existing keyed configurations retain their runtime behavior; the prior `AutoMigrate: false` value was normalized to enabled and therefore could not express an opt-out. The additive public field can break unkeyed struct literals at compile time, so those callers must migrate to keyed literals before upgrading. No persisted-data format or minimum-Go-version change is implied. |
| DL-011 | 2026-07-18 | Treat accepted local work, including delayed workflow descendants, as a shutdown drain obligation bounded by the supplied context. | Shutdown may now wait for accepted delayed work and return the context error when the deadline expires; cleanup remains retryable and a later call converges. This is a runtime-lifecycle correction with no API, configuration, persisted-data, or minimum-Go-version change. |
| DL-012 | 2026-07-18 | Make successful queue shutdown terminal while keeping incomplete cleanup retryable. | `Dispatch` and `StartWorkers` now reject use after successful shutdown instead of reporting false success over closed resources; construct a new queue instance to restart. Repeated shutdown is idempotent. This is a runtime-behavior correction with no source/API, configuration, persisted-data, operational-migration, or minimum-Go-version change. |
| DL-013 | 2026-07-18 | Fence every SQL processing generation with a random token and require that exact claim to finalize the row. | This is a persisted-schema and operational rollout change, not a source/API, configuration, wire-envelope, or minimum-Go-version break. The nullable `processing_token` column preserves existing rows and old producer-only binaries. Externally managed schemas must add it before new workers start. Quiesce every old SQL worker before migration and then start the new worker fleet; mixed old/new workers are unsafe because old workers settle by row ID and cannot honor the generation fence. Rollback likewise requires quiescing new workers before running an old worker binary. |
| DL-014 | 2026-07-18 | Keep the canonical public workflow models physically owned by root `queue`, with explicit private adapters around one `internal/workflow.Engine`; never expose an `internal` package as the real owner of a public alias. | Source-compatible `bus` model, middleware, and store names now resolve to root types. Legacy `bus.Job`, `Event`, `Observer`, `Bus`, `Option`, and self-returning builder interfaces remain physical compatibility contracts. Existing code that keys behavior on `%T`, `reflect.Type.PkgPath`, gob/interface registration names, generated type registries, DI keys, or custom type-sensitive persistence must migrate applicable names from `github.com/goforj/queue/bus` to `github.com/goforj/queue`. JSON/wire envelopes, SQL schemas/data, runtime outcomes outside DL-015, operational rollout, and the minimum Go version do not change. |
| DL-015 | 2026-07-18 | Make every `bus.New(*queue.Queue)` a view of the root engine, reject view-specific construction options, and preserve the option-bearing raw-runtime route. | This is a configuration and runtime-behavior incompatibility, not a source/API break. Root and bus views now share registrations, store, observer, middleware, and lifecycle; code requiring isolation must use distinct runtimes. Move observer, store, clock, and middleware options to root queue construction, then call option-free `bus.New(existingQueue)`. Preserving the old behavior would preserve the second engine this milestone removes. |
| DL-016 | 2026-07-18 | Dispatch ordinary root jobs by their application type and exact payload, carrying correlation in one versioned driver metadata record instead of the workflow envelope. | Existing root and bus signatures remain source-compatible; advanced metadata helpers and `WithLegacyDirectEnvelope` are additive. Wire behavior changes for root direct jobs, SQL adds nullable `queue_jobs.metadata_json`, and absent payload no longer becomes JSON `null`; arbitrary raw bytes reach handlers without a dispatch-time JSON re-marshal. New workers read old and new deliveries, but old workers cannot safely consume new direct types. Deploy new workers while producers retain legacy emission, switch producers only after all consumers are upgraded, and restore legacy emission plus drain direct backlog before worker rollback. Raw-runtime bus v1 bytes, workflow envelopes, uniqueness keys, configuration files, and the minimum Go version remain stable. |
| DL-017 | 2026-07-18 | Make `queue.NewFake` the sole fake-state owner and reduce `bus.Fake` and `queuefake.Fake` to compatibility views over its direct and workflow records. | Constructor and method signatures, the usable `bus.Fake` zero value, value copyability, and physical `bus.Fake`/`bus.BatchSpec` identities remain source-compatible. Testing runtime behavior is intentionally corrected: queue and workflow compatibility views share direct history and effective default queues; abandoned builders, invalid jobs, and canceled dispatches do not record; builder options survive; chain/batch IDs are opaque lookup identifiers instead of `fake-chain`/`fake-batch`; `FindChain`/`FindBatch` expose pending fake state; `Reset` clears direct, workflow, and store state; and fluent closure callbacks are not retained in fake runtime state or executed. Preserving separate histories or constructor-time records would preserve the duplicate owners and false-positive assertions this milestone removes. Tests that require isolated histories must use distinct fake instances; tests must treat returned IDs as opaque and may inspect them through `FindChain` or `FindBatch`. No configuration, persisted-data, wire-format, operational-rollout, or minimum-Go-version change is implied. |
| DL-018 | 2026-07-18 | Reject ambiguous workflow creation records and make memory-store chain snapshots caller-independent. | This intentionally tightens runtime input validation for direct `WorkflowStore` calls without changing source/API, configuration, persisted schema, wire format, or the minimum Go version. Chain and batch IDs must be non-empty, each record must contain at least one member, and member IDs must be non-empty and unique; the public builders already satisfy these constraints. Direct callers must correct invalid records before upgrading. Memory-store callers must no longer rely on mutating input or returned chain payloads to mutate stored state. Existing persisted rows are not rewritten, but deployments that previously wrote ambiguous chain records directly should audit and repair them before further processing. |
| DL-019 | 2026-07-18 | Require `VARBINARY` for every MySQL workflow identity column and derive accepted widths from the complete connected schema. | This is a MySQL persisted-schema, runtime-behavior, and operational migration correction; it is not a source/API, configuration-file, wire-envelope, or minimum-Go-version break. Fresh auto-schema uses byte-exact 255-byte workflow/member and receipt identities plus 512-byte callback keys. When a legacy schema has no receipt table, automatic startup validates the existing `VARBINARY` identity columns and derives a shared receipt wide enough for both effective workflow-ID capacities and both member-ID capacities. `TestWorkflowStoreIntegration_MySQLAutoMigratesMissingReceiptAtLegacyWidths` proves ordinary startup preserves a live 512-byte legacy schema and accepts long chain, batch, member, callback, and receipt identities. Existing receipt tables are never altered; their capacities intersect the accepted limits of the complete connected schema. Deployments with an incompatible existing receipt must quiesce workflow writers, audit comparison-equivalent and over-limit identities, migrate the table, and construct a fresh store for capacity rediscovery. Extremely wide legacy identities can produce a derived primary key beyond the server's indexed-key budget; startup then reports both widths and schema-first guidance instead of silently narrowing or altering tables. Operators must precreate a compatible indexed schema or explicitly migrate supported limits and existing data before rollout. Managed `VARCHAR`, `TEXT`, and fixed-width `BINARY` columns still fail instead of silently conflating or padding identities. |
| DL-020 | 2026-07-18 | Separate queue-generation provenance, workflow-transition receipts, and the future settlement/continuation outbox. | Each SQL claim carries an opaque generation ID. Same-attempt redelivery normally retains inherited recovery provenance; after the current generation durably claims a transition receipt, the additive delivery-settlement application-state signal makes SQL retain that current generation if later infrastructure still requires redelivery. The signal is not queue settlement or observer delivery, and application retry clears the link. Direct built-in stores record the private immutable receipt in the same transaction as the chain-node or batch-member transition. `receipt_version` versions durable ownership and `event_schema_version` versions the shared observer facts independently from workflow-envelope protocol; both start at `1`, and unsupported values fail closed with an uncommitted outcome. Logical receipt proof requires a complete persisted owner, including a nonnegative owner attempt, matching workflow incarnation/member, dispatch, and job fingerprint plus nonempty current dispatch/`JobID`. The current attempt is physical provenance and may differ from the owner or be negative; chain physical `JobID` may also differ, while batch `JobID` remains the logical member key and must match. That logical proof suppresses handler replay. Reconstructing facts additionally requires the exact prior recovered generation, current attempt, and physical `JobID` owner tuple. A SQL aggregate row must own completion and a cancelled aggregate must own failure; when it names the requested logical member, its incarnation, complete physical owner, and outcome must match that member receipt, and its flags must agree with live terminal state. Contradictions fail uncommitted before effects. Validated durable predecessor success restores its immediate live continuation without handler replay when receipts are absent/hidden or carry non-exact physical provenance, but those paths emit no predecessor facts or callbacks; progressed and terminal state is a no-op, and duplicate successor enqueue remains possible. Failed chain receipts return the first persisted cause as permanent across physical nonowners without replaying callbacks or occurrence-based failure facts; an empty cause becomes a permanent diagnostic. Failed batch receipts return a generic permanent cause across the same physical variants because their original cause is not persisted. Both keep duplicate physical rows on the archive path without fabricated failure facts. Built-in `FailChain` now preserves the first terminal cause rather than allowing later calls to overwrite the authoritative value used by failed-receipt recovery. Direct store callers that relied on late failure-cause replacement must retain that metadata separately. This is a runtime-behavior tightening, not a source/API, configuration, persisted-schema, wire, or minimum-Go-version change. `claimedNow` is response-local, and queue provenance or aggregate state alone is insufficient. Real SQLite, MySQL, and PostgreSQL finalization-failure tests prove supported-version successful terminal-chain recovery; focused memory/SQLite contracts plus a repeated real SQLite archive-failure fixture prove atomic terminal-failure receipts, first-cause archive, and lineage repair. SQLite additionally proves definite chain-successor rejection recovery and failed-batch archive, while the compatibility-focused successor test covers receipt-absent, decorated, and non-exact provenance. Focused corruption contracts pin aggregate incarnation, completion, cancellation, owner, outcome, and member-presence checks. Real twelve-worker MySQL/PostgreSQL races and the SQLite two-member recovery prove only one serialized member owns aggregate terminal effects. This slice otherwise adds `busruntime` API and root runtime behavior without removing a root API or changing configuration files, application wire, or the minimum Go version. It adds `NewSQLStoreWithManagedSchema` because legacy `NewSQLStore` continues enabling migration despite the false `AutoMigrate` zero value, and it adds persisted `bus_workflow_transition_receipts`; managed deployments require schema-first rollout and quiescent worker rollback. Old binaries ignore a retained table, while dropping it loses provenance and old pruning can leave orphan rows. Custom/decorated/raw stores keep public compatibility but have weaker private guarantees outside state-confirmed chain-successor liveness. Successful recovery facts use deterministic IDs; failures remain occurrence-based. The receipt is not a settlement outbox or durable continuation/callback intent. Server-dialect failed-chain finalization evidence, conservative successor re-dispatch's duplicate ambiguity, and physical commit/readback ambiguity remain open. |
| DL-021 | 2026-07-19 | Treat coverage as a repository-wide multi-module and backend fan-in contract. | Unit coverage runs every buildable module independently with `GOWORK=off`; each existing backend matrix leg emits one integration profile from the actual integration module. A final guard rejects missing, extra, malformed, duplicate, or non-executing backend evidence before one explicit Codecov upload. This changes CI evidence only; it does not change source/API, configuration, persisted data, runtime behavior, wire formats, operations, or the minimum Go version. |
| DL-022 | 2026-07-19 | Close coverage gaps with deterministic behavioral tests without adding production-only test seams. | Scripted failures, focused driver contracts, and a targeted RabbitMQ integration scenario now prove reachable migration, lifecycle, settlement, uniqueness, retry, and shutdown boundaries. Explicit defensive tests pin fail-closed behavior for malformed collaborator results without presenting those results as production client behavior. Fixed-structure JSON marshal failures, entropy-source failures, and failures emitted only inside concrete broker clients remain uncovered where exercising them would require global hooks or production-only indirection. This preserves runtime design and test isolation while making the remaining coverage limits explicit. |
| DL-023 | 2026-07-19 | Require opaque physical identity before `settlement_failed` can close a `StatsCollector` active execution. | The source-compatible `busruntime.DeliverySettlementIdentity` type and context accessor are additive public API. Every built-in settlement-aware driver forwards the same handler context through start, process, and settlement facts. Older or custom drivers that omit it may conservatively overcount `Active`; guessing from event fields could undercount a newer execution after a late settlement. Release and upgrade the settlement-aware driver modules with root when exact gauges matter. This changes metrics runtime behavior and adds an operational rollout consideration, but does not change configuration, persisted data, wire formats, or the minimum Go version. |

## Progress Log

### 2026-07-19

- Fixed all five primary findings from the third fresh-context review: canonical physical queue labels now agree across queue, worker, and workflow facts; every driver module has an independent parallel race job; manual observer snippets compile against the context-aware signature; settlement failure closes only its exact active execution; and SQL shutdown retries share one bounded drain waiter.
- Closed the post-fix continuity edges found by three specialist audits. Whitespace-only explicit queues retain existing physical routing across every event layer, identity-less late settlements cannot consume a newer execution, handler panics emit truthful failure telemetry before rethrow, metrics documentation uses the canonical physical queue contract, and the historical aggregate `race` check remains available for branch protection while the module checks stay parallel.
- Fixed all six findings from the second fresh-context PR review. Nil handler registration is a no-op at every root, workflow, and deprecated facade boundary; direct, legacy-envelope, and raw compatibility paths return the normal missing-handler error instead of panicking, and nil cannot replace a valid handler.
- Made Redis owned-resource shutdown converge after a close diagnostic. The first cleanup caller receives every joined resource error, later calls return success without closing anything twice, concurrent callers remain race-safe, and the public queue leaves draining on retry.
- Corrected observer migration guidance so legacy workflow sinks retain the three queue-layer dispatch facts, and aligned the metrics taxonomy with the runtime's deliberate event-layer mapping.
- Added an executable generated-documentation guard that runs every deterministic generator, verifies checked-in README, examples, test-count badges, and benchmark dashboards, and proves second-run idempotency in both CI and the all-module gate. Unit counts come from the current executed suite; integration counts combine the all-backend integration module with root integration-tagged tests. Their full-run manifest hashes every Go and module input in the integration module plus root tagged sources and module inputs. Full regeneration rejects partial backend selection and disables optional chaos/soak modes, so unit CI cannot silently preserve stale, reduced, or out-of-module integration evidence.
- Replaced the duplicate-idempotency scenario's two independent dispatches with one job whose first handler attempt commits its keyed side effect and forces a real driver retry. Redis advances its isolated real retry entry deterministically instead of sleeping through randomized production backoff.
- Made chaos and flake-repeat jobs actually run on the weekly schedule. The repeat harness now requires one named backend and validates the exact scenario's `go test -json` terminal event, records capability skips separately, and fails missing execution instead of reporting a false pass. The Redis chaos test stops the broker while a handler is active, proves its successful result cannot be acknowledged, retains the same active task, exercises Asynq's real lease-expiration recovery, preserves the zero application retry budget, redelivers exactly once, and settles with one side effect. The exact scheduled Redis subset and three repeated lost-ack runs pass locally.
- Completed three independent fresh-context reviews of public compatibility, runtime correctness, and test/CI evidence. No new runtime correctness defect survived validation.
- Corrected the README-linked direct-delivery guide so managed SQL schemas add both queue columns and old/new SQL worker generations never overlap during upgrade or rollback.
- Replaced root-only Codecov input with deterministic coverage from every buildable module and all ten parallel backend jobs. The fan-in guard now proves exact artifacts, normalized unique ranges, complete module inventory, and backend-specific executed functions before upload.
- Restored the repository's established Codecov project and patch status policy, made upload failures fail CI, and documented the multi-module collector without implying that coverage replaces behavioral guarantees.
- Used the first complete fan-in report to add focused SQL, NATS, Redis, RabbitMQ, and SQS failure-path tests. SQL combined changed-statement coverage reached 98.3%, NATS 93.3%, Redis 95.7%, SQS 96.3%, and RabbitMQ 90.7% without production hooks or local containers.
- Followed the fan-in report with an honest boundary pass that proves NATS shutdown waits for accepted work, Redis state ownership reaches real command semantics, and RabbitMQ immediate retry advances, commits, and leaves its broker queue empty. Defensive invariant tests verify ambiguous SQL claim results roll back and absent SQS client results fail closed. Fresh-context review separated supported behavior from defensive coverage and removed tests whose only premise was invalid injected collaborators.
- Added the lightweight root integration-tagged `bus` fixture suite to unit collection and made the fan-in guard require proof that it executed.

### 2026-07-18

- Completed the initial architecture, public API, workflow, lifecycle, driver, observability, documentation, and multi-module audit.
- Reproduced the transient workflow retry contradiction and public `UniqueFor` failure.
- Established the north-star model, compatibility guardrails, decision gates, milestones, and validation matrix.
- Accepted one canonical queue/workflow surface, one observer/event model, job-based durable continuations, and direct job execution without workflow wrapping.
- Repaired the NATS, SQS, and RabbitMQ observer test drift; all three full module suites and the repository-wide module compile guard pass independently with `GOWORK=off` for nested modules.
- Added the examples module's independently required dependency graph and verified every generated/manual example build with `GOWORK=off` without further module edits.
- Introduced the first unified-observer slice: root `Event` now carries every layer's correlation fields, `WithObserver` receives queue/worker/workflow events, and the root `Workflow*` observer names are deprecated canonical aliases.
- Replaced copied mutex-bearing runtimes with shared lifecycle state across context-bound handles; the root vet pass is now clean.
- Expanded the all-module validation entrypoint to cover tests, vet, examples, integration, and the docs tooling module, and made CI run the full independent-module pass.
- Added black-box public `Queue` contract fixtures for direct jobs, chains, batches, retries, uniqueness validation, unified observation, lookup, and lifecycle sharing.
- Replaced nested observer wrappers with one concurrency-safe sink retained by root, Redis, SQL, NATS, SQS, and RabbitMQ paths; config and option observers now share event identity and late options reach native driver events.
- Added transitional, schema-gated logical-envelope decoding so queue, worker, workflow, Redis, and broker-republish facts share job type and dispatch/job/chain/batch correlation without leaking volatile wrapper IDs into observability `JobKey`. Driver-enforced `UniqueFor` remains open under M1-05/M1-06.
- Classified dispatch/enqueue/control as queue facts, physical attempts as worker facts, and logical job/chain/batch/callback transitions as workflow facts so `Event.Layer` follows one semantic rule instead of the package that happened to emit an event.
- Accepted the acyclic consolidation sequence `queue -> internal/workflow`, with `bus` retained only as a staged compatibility facade and no new public workflow model.
- Added the first D-001/M2-01 foundation in `busruntime`: one tested attempt classifier now distinguishes retryable application failures, terminal failures, and infrastructure outcomes that require same-attempt redelivery without changing existing handler signatures or runtime behavior.
- Propagated physical attempt metadata through both root adapters and every worker reconstruction path, then corrected chain/batch transitions so public Sync workflows can fail transiently and later complete without stale failed state or premature callbacks.
- Split local enqueue acceptance from inline execution, gated workerpool delivery until acceptance observation completes, and locked the exact Sync success/failure sequence so handler errors are no longer mislabeled as enqueue rejection.
- Added a 12-module workspace/replacement/release/tag guard and brought the docs tooling module into `go.work`.
- Found a release blocker: published nested modules require siblings at nonexistent `v0.0.0` versions. Keep relative replacements for repository testing, but pin every sibling requirement to the prospective release before the next tag family is created.
- Removed the blanket at-least-once documentation claim: the evidence matrix now distinguishes fixture coverage from production guarantees, calls Core NATS explicitly ephemeral, records RabbitMQ's missing publisher-confirm boundary, and no longer presents fixture contention tests as proof of public `UniqueFor`.
- Fast-forwarded to `origin/main` at `18a7647`, preserved its retired-badge removal during regeneration, and moved the reconciled work to `refactor/unify-queue-workflow` for scoped commits and later PR/CI validation.
- Made permanent outcomes operational across local, Redis, SQL, NATS, SQS, and RabbitMQ workers, and introduced a distinct uncommitted outcome for infrastructure/workflow-state failures. Redis's original final-attempt gap is now covered for newly reserved tasks, while legacy queued tasks retain the upstream limitation.
- Deferred logical job and chain/batch terminal facts until the owning workflow mutation commits. Chain, batch, and callback store failures now return the uncommitted outcome, suppress premature callbacks/events, preserve the store cause, and have exhausted-attempt recovery/idempotency regressions.
- Separated synchronous continuation failure from its predecessor's physical outcome: a downstream chain node can return its exact error to the caller without retrying the already-successful node or corrupting failed state into completion.
- Verified the reconciled branch with the full 12-module test-and-vet matrix, independent nested modules under `GOWORK=off`, root race tests, README snippet compilation, the module inventory guard, and stable README/example generation. Integration test-count discovery timed out at its bounded 30-second limit and deliberately retained the existing integration badge rather than fabricating a count.
- Defined one canonical, versioned logical uniqueness identity from effective queue, application job type, and exact payload; workflow correlation IDs and delivery options no longer defeat public `UniqueFor`.
- Replaced duplicated in-memory uniqueness maps with one token-owned store, added pre-acceptance compensation across local and broker drivers, coupled SQL claims to queue-row insertion, and added a backend-shared public facade scenario.
- Added backend-shared Redis logical claims while retaining Asynq physical claims for direct-job rollout compatibility; documented the non-atomic claim/enqueue boundary and coordinated public-workflow rollout requirement.
- Preserved explicit workflow `Retry(0)` instead of falling through to backend defaults, required SQS message receipts and RabbitMQ publisher confirms before replacement settlement, surfaced RabbitMQ/SQS settlement ambiguity, and made closure callback failures observable instead of silently successful.
- Added a header-marked Asynq transport reserve so new Redis tasks can redeliver an uncommitted final application attempt without inflating handler-visible retry counts; terminal outcomes explicitly skip the reserve and lease recovery does not consume it. A container-backed test now proves `Retry(0)` redelivers the same application attempt through the real v0.26 processor.
- Pinned the Redis retry-budget boundary through a real broker: task storage carries `application retries + 1`, the versioned header retains the application value, and worker observation restores that original value for handlers and users.
- Proved canonical Redis uniqueness across concurrent public clients, producer shutdown/restart, and TTL expiry; proved the same public logical composition across concurrent and restarted SQLite clients, and pinned the persisted `v1` key with a golden vector.
- Deferred SQL, SQS, and RabbitMQ positive process/workflow facts until their settlement owner commits. Missing or failed settlement now suppresses success and emits `settlement_failed` with the original delivery attempt; SQL finalization retries are bounded and require exactly one affected row.
- Prevented callback redelivery from converting a prior callback failure into success, skipped absent optional callback deliveries, and made missing ephemeral callback state fail visibly instead of reporting a no-op success.
- Made the Redis timeline/uniqueness store structurally required, validated Asynq's one-second unique TTL before claiming, and closed all owned producer state resources.
- Added an explicit MySQL expiry-index migration probe so existing uniqueness tables receive bounded-pruning support rather than only new installations.
- Normalized absent, zero-byte, and exact JSON `null` payloads into one pinned uniqueness identity so the eventual direct-job cutover cannot split existing workflow claims.
- Made allowed-failure batches derive completion from aggregate state regardless of failure order, proved the behavior through the public facade, and ensured Catch, Then, and Finally each execute once.
- Validated callback workflow state before consuming idempotency markers, isolated callback and Progress panics, removed duplicate orphan starts, and exercised real serialized Catch/Then/Finally envelopes in reverse order.
- Reworked root lifecycle coordination so concurrent starts and shutdown share attempts, failed cleanup remains retryable, never-started producers close, and post-close work is rejected.
- Preserved caller ownership of supplied SQL handles, propagated owned close errors, and added a real SQLite ownership proof alongside Redis owned-resource coverage.
- Leased every root operation that can touch runtime resources, including dispatch, readiness, pause/resume, stats, administration, and history, so shutdown cannot close resources underneath an admitted call.
- Replaced the process-global continuation marker with runtime-scoped, non-transferable permits that expire when a handler returns; foreign and escaped contexts can no longer enqueue after drain begins.
- Added a post-worker quiescence barrier so a descendant admitted during drain finishes before producer cleanup, and gave direct SQL runtimes their own scoped permit rather than trusting a caller-forgeable generic marker.
- Retained partially started external workers for retryable cleanup and installed one stable, replaceable handler slot per job type, so canceled Redis/Asynq startup can retry, same-key replacement remains continuous across startup races, and a started strict mux never receives a duplicate pattern.
- Latched shutdown intent before waiting on in-flight startup, preventing a fresh start or dispatch from overtaking cleanup while the original start is still blocked.
- Made Sync and Workerpool reserve accepted delayed descendants through shutdown, gave bounded workerpool callbacks a reentrant relay that avoids one-worker Catch/Finally deadlock, and made `WithWorkers` control local execution concurrency.
- Made NATS worker startup retryable and subscription-flush-gated, synchronized real Core NATS drain completion, retained the producer connection for admitted callback/delay work, and proved a real queued callback backlog drains. Core NATS remains an ephemeral broadcast adapter whose retry replacement can be accepted without a subscriber, so D-004 is still open.
- Kept workflow queue, job type, and logical `JobKey` correlation across aggregate/callback events, and made allowed batch failures emit progress rather than a false terminal failure before later completion.
- Propagated the first workflow job's queue, type, and logical key into chain/batch start facts, and preserved the triggering job's physical payload metadata through callback envelopes so the unified observer sees one continuous identity.
- Made `settlement_failed` end the collector's active attempt without inventing a processed or application-failed count.
- Added an explicit `DisableAutoMigrate` path across database wrappers, removed poisoned one-shot SQL startup, proved no schema is created when migration is disabled, and proved startup can recover from a real SQLite schema lock.
- Added per-claim SQL processing tokens, invalidated them on stale recovery and administrative transitions, and required token-matched finalization so an expired handler cannot delete, retry, archive, overwrite, or report success for a row already reclaimed by another worker. Real SQLite tests cover legacy-schema migration plus stale success and stale failure races across two runtimes.
- Began M2-07 with one private workflow-protocol owner shared by root identity/observability and orchestration dispatch. Literal version-one fixtures freeze schema/type names, malformed and unknown-version fallback, legacy JSON payload semantics, transport options, and all chain/batch callback delivery routes before the engine moves.
- Seeded the exact legacy workflow SQLite DDL and persisted records from literal SQL, then proved the current store reads and mutates them without rewriting `nodes_json`, losing active/recent state, duplicating callbacks, or changing terminal-prune semantics.
- Validated the frozen slice with the full 12-module test/vet gate, root and every concurrency-sensitive driver under the race detector, README snippets, stable two-pass generation, the complete local/SQLite integration matrix, and real container-backed Redis and NATS lifecycle scenarios.
- Moved the cohesive chain, batch, callback, middleware, event, and store implementation behind one `internal/workflow.Engine`; root production no longer imports public `bus`, and an architecture guard prevents that dependency from returning.
- Established physical root workflow records, stores, middleware, messages, and results with explicit root-to-engine adapters so GoDoc, reflection, code generation, and custom stores see `queue` rather than an inaccessible internal package.
- Rebuilt public `bus` as a deprecated compatibility facade: option-free `bus.New(existingQueue)` shares the configured root engine, the raw-`busruntime.Runtime` seam delegates to the same internal engine, construction-only options are rejected explicitly on existing queues, and source fixtures retain custom Bus/Store/Middleware/builders, Temporal, fake, composite-literal, and payload behavior.
- Removed the second observer producer. Legacy `bus.Event` values are translated only for compatibility consumers while one root observer receives delivery and workflow facts.
- Completed M2-07 after direct tests covered both store-adapter directions, physical middleware branches, package identities, queue/raw facade builders and lifecycle, exact error propagation, nil/empty payload ownership, and v1 Dispatch-time payload encoding. Revalidated all 12 modules with vet and independent nested-module resolution, root and concurrency-sensitive drivers under the race detector, stable two-pass example/README generation, README snippets, the local/SQLite integration matrix, and real container-backed Redis and NATS suites.
- Completed M2-05/M2-10: ordinary root jobs now retain their application type and exact payload, while one versioned correlation record travels through in-memory jobs, Redis headers, NATS/SQS/RabbitMQ messages, and nullable SQL storage. The engine registers both direct application handlers and legacy envelope handlers, so middleware, retry classification, logical events, settlement deferral, queued v1 work, reserved protocol names, and raw-bus wire fixtures remain unified.
- Added the workers-first migration gate `WithLegacyDirectEnvelope`, additive concurrent-safe SQL metadata migration, caller-managed-schema diagnostics, malformed/future metadata fallback, backend retry/republication identity tests, and shared integration assertions that the dispatch receipt matches the delivered `Message` across local, SQLite, Redis, and NATS execution.
- Completed M5-07 by moving all fake state into `queue.FakeQueue`, adapting the deprecated bus and queuefake surfaces onto it, and running fake workflows through the production engine without retaining non-executing closure callbacks in fake runtime state. Focused tests cover execution-time recording, validation and cancellation failures, builder reuse and policy, payload isolation, lookup/reset behavior, the legacy zero value, shared compatibility views, and concurrent access under the race detector.
- Completed M3-04/M3-05 with one additive first-writer `WorkflowOutcomeStore` used by memory, SQL, fake, root, and deprecated compatibility paths. Chain success/failure compare-and-swap the same ordered node, batch member outcome categories remain immutable, and losing redeliveries publish no contradictory job/workflow facts, progress, or callbacks. Concurrent outcome races, claim rollback faults, legacy dual-terminal rows, fail-fast settlement, callback claims, caller ownership, and repeated real SQLite, MySQL, and PostgreSQL contracts now cover the transition boundary. MySQL auto-schema uses byte-exact dialect types, rejects fresh-schema truncation, and rejects non-`VARBINARY` managed identity columns. A real legacy-width integration now drops only the receipt table beside 512-byte state identities, proves ordinary startup derives a 512/512 replacement without altering existing tables, and exercises identities above the fresh defaults. The separate managed-width fixture proves complete pre-existing schema discovery.
- Added opaque SQL delivery-generation provenance and separate built-in workflow transition receipts without conflating either with continuations or observer delivery. Forced SQLite, MySQL, and PostgreSQL finalization failures prove supported receipts suppress duplicate application execution while exact recovered-generation ownership gates reconstructed success facts. Focused memory/SQLite store and runtime contracts add terminal chain-failure receipts atomically, return the first persisted permanent cause across exact, different, or legacy generation provenance, suppress repeated handlers/callbacks/failure facts, roll back parent failure when receipt insertion fails, and fail invalid receipts closed. Built-in `FailChain` now preserves that first cause; direct callers that used late calls as replacement metadata must retain those diagnostics separately. A real SQLite chain-failure fixture forces the initial archive and multiple recovery archives to fail, retains owner/attempt-zero/cause through fenced best-effort repair, and then reaches `dead` at attempt one with a single application/workflow occurrence. SQLite additionally covers completed predecessors, later-attempt ownership, aggregate non-inference, a two-member completing-owner recovery, and a failed batch member that archives with a generic durable cause. Active chain recovery re-dispatches an immediate successor after definite rejection without replaying its predecessor. A compatibility matrix now proves the same liveness-only behavior for missing receipts, decorated stores, and different/legacy generation provenance, with no predecessor facts/callbacks and no dispatch after successor progress or terminal state. Duplicate enqueue remains possible while successor progress is not durable intent. Real twelve-worker MySQL and PostgreSQL fail-fast races prove the locked parent transition produces one aggregate owner and one terminal fact pair. `receipt_version` and shared observer `event_schema_version` both start at `1`, evolve independently from the workflow-envelope protocol, and fail recovery closed when unsupported. Application retry clears the earlier generation link, while the delivery-settlement application-state signal preserves a current receipt owner when later infrastructure requests another same-attempt redelivery. Server-dialect failed-chain finalization evidence, remaining custom/decorated/raw-store parity, durable callbacks/continuations, settlement outbox, managed-schema/pruning gates, and physical commit/readback ambiguity remain open.
- Final receipt hardening separates logical transition proof from exact physical fact ownership. A complete persisted owner still requires a nonnegative attempt, but a duplicate's current attempt may differ or be negative; chain physical `JobID` may differ, while batch `JobID` remains its logical member. These nonowners suppress handlers and facts, preserve only a live chain successor, and keep failed chain/batch deliveries on their permanent archive path. SQL aggregate readback now fails closed for stale incarnation, missing completion/member, success-owned cancellation, or owner/outcome disagreement with the member receipt; runtime flags must also match live terminal state.

## Next Action

Continue **M1-01/M1-04/M3-09** with managed-schema migration/rollback plus real cross-dialect pruning and physical commit/readback ambiguity gates, and explicit custom/decorated/raw-store fallback contracts. Then expand opaque generation provenance across settlement owners where the backend can support it. In parallel, specify the separate driver-owned settlement outbox and persisted callback/continuation intents needed when no delivery survives. Keep exact successor-enqueue ownership despite conservative at-least-once recovery, partial batch fan-out, Redis claim/enqueue, Core NATS ephemerality, SQS visibility extension, and RabbitMQ reconnect/context-aware lifecycle gaps explicit.
