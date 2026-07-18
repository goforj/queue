# Queue Unification Plan

Status: Active

Last updated: 2026-07-18

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
- `bus` becomes a deprecated compatibility facade over the canonical queue model. It must not retain an independent Job, handler, lifecycle, store, fake, or observer implementation.
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

Asynq v0.26 checks retry exhaustion before consulting its `IsFailure` predicate. Redis can therefore preserve an uncommitted attempt only while the transport retry counter remains below its limit; an uncommitted final attempt is currently archived. Do not claim complete same-attempt redelivery for Redis until M1-04 defines a mixed-version-safe reserve or replacement-delivery protocol and proves it against the real Asynq processor.

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

- replace unkeyed `Event` literals with keyed literals because the canonical event envelope gained correlation fields;
- adapt custom `bus.Observer` implementations with `queue.ObserverFunc`, or keep direct legacy bus consumers on `bus.WithObserver` until `bus` becomes a forwarding facade;
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
5. route option-free `bus.New(*queue.Queue)` through root and reject construction-only options explicitly instead of silently ignoring them.

The extraction itself is source/API, configuration, persisted-data, runtime-behavior, operational, wire, and minimum-Go-version neutral. Later alias conversions require focused source-compatibility fixtures before landing.

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

- [ ] **M1-01 — Add retry-state regressions.** Cover chain and batch transient success, terminal exhaustion, fatal errors, attempt numbers, callbacks, and event ordering through public queues. Public transient, exhaustion, permanent, attempt, callback, ordering, downstream-node, and workflow-store fault cases are in place; real settlement-owner and cross-driver store-failure cases remain.
- [x] **M1-02 — Stop premature workflow failure.** Chain and batch state, callbacks, and terminal logical events now wait for a permanent or exhausted outcome; retryable attempts remain worker-layer failures.
- [x] **M1-03 — Make `FailOnError` operational.** `queue.Permanent`, `FailOnError`, and the shared classifier now stop application retries across local, Redis, SQL, NATS, SQS, and RabbitMQ paths.
- [ ] **M1-04 — Emit only committed outcome events.** Workflow mutation failures are classified as uncommitted before terminal facts publish, retry facts appear only when a numbered retry delivery begins, and generic/archive predictions were removed. Real settlement confirmation, callback-dispatch recovery, Redis final-attempt redelivery, and NATS's explicit nonconformance remain.

### Identity and uniqueness

- [ ] **M1-05 — Define logical job identity.** Keep volatile dispatch/job IDs out of the uniqueness key while retaining correlation metadata.
- [ ] **M1-06 — Repair public `UniqueFor`.** Add same-process, multi-producer, restart, and failed-first-dispatch tests according to each driver's declared capability.
- [ ] **M1-07 — Make uniqueness acquisition atomic with acceptance.** Use backend transactions or release/compensation where a single atomic operation is unavailable.

### Configuration and lifecycle behavior

- [ ] **M1-08 — Apply `DefaultQueue` centrally.** Preserve explicit queue names and ensure empty names follow one documented rule across every driver.
- [ ] **M1-09 — Make worker targeting explicit.** Implement D-005 and test that every accepted target is consumed by the intended runtime configuration.
- [ ] **M1-10 — Make `WithWorkers` effective.** Verify actual concurrency for workerpool, SQL, Redis, NATS, SQS, and RabbitMQ rather than wrapper state.
- [ ] **M1-11 — Clarify sync startup semantics.** Either register bus handlers immediately for synchronous dispatch or consistently require startup and correct every example and contract.
- [ ] **M1-12 — Validate registrations and options.** Reject nil handlers and nil options deterministically; preserve explicit zero versus unset retry, timeout, and backoff values.
- [ ] **M1-13 — Normalize payload contracts.** Give `Payload` and `PayloadJSON` distinct, documented behavior and consistent binding errors.

### Readiness, capabilities, and shutdown

- [ ] **M1-14 — Restore backend readiness.** Forward readiness through every bridge and add negative unreachable-backend tests.
- [ ] **M1-15 — Replace wrapper-inflated capability checks.** Report actual capabilities independently of observers and adapters.
- [ ] **M1-16 — Make shutdown retryable and context-aware.** Do not discard lifecycle/resource state before cleanup succeeds.
- [ ] **M1-17 — Close producer-owned resources without worker startup.** Cover SQL and Redis producer-only lifecycles and externally supplied resource ownership.
- [ ] **M1-18 — Drain before broker settlement resources close.** Verify in-flight SQS deletes and RabbitMQ acknowledgements during shutdown.

Exit criteria:

- The retry, uniqueness, default queue, worker count, readiness, capability, and shutdown contracts pass for every applicable driver.
- No option changes meaning during queue-to-workflow-to-driver conversion.
- Enabling an observer cannot change reported capabilities or job outcomes.

## Milestone M2: Establish Clean Internal Boundaries

Objective: introduce a stable internal architecture while preserving the root application API.

- [ ] **M2-01 — Specify the driver SPI.** Define immutable enqueue input, option presence, logical identity, delivery attempt metadata, settlement outcomes, lifecycle, and capabilities.
  - [x] Add the additive attempt-classification foundation shared by orchestration and delivery: zero-based attempt metadata, success/retry/failure/redelivery decisions, and distinct permanent versus uncommitted error markers.
  - [x] Propagate attempt metadata through every root/driver handler path before changing workflow transitions; Redis now preserves queue/attempt/retry metadata even without observers, and SQL reconstruction has a direct contract test.
  - [ ] Move committed retry/archive emission to each settlement owner and define enqueue acceptance receipts.
- [ ] **M2-02 — Create a domain-neutral core/SPI package.** Drivers depend on this package rather than importing root helpers through re-export and global hook layers.
- [ ] **M2-03 — Introduce adapters alongside existing drivers.** Migrate one local and one durable driver first, keeping compatibility tests on both paths.
- [ ] **M2-04 — Remove the mutable runtime hook bridge.** Retire `any`-based global initialization only after every driver uses the new SPI.
- [ ] **M2-05 — Consolidate the Job model.** Keep one canonical public job specification and separate it clearly from internal envelopes and delivered messages.
- [ ] **M2-06 — Version the envelope.** Define schema evolution, unknown-version behavior, mixed producer/worker deployments, and rollback.
- [ ] **M2-07 — Resolve the public `bus` direction.** Implement D-003 and D-010 in bounded slices: extract `internal/workflow`, remove root production imports of public `bus`, establish physical root models, then make `bus` a deprecated forwarding facade with source- and wire-compatibility fixtures.
- [ ] **M2-08 — Separate producer and worker lifecycle.** Model start, running, draining, stopped, and failed states without `sync.Once` poisoning.
- [x] **M2-09 — Collapse root observers.** One root observer receives delivery and workflow events through a shared sink without duplicate execution events. The legacy public `bus` observer remains until M2-07 makes that package a forwarding facade.
- [ ] **M2-10 — Stop enveloping direct jobs as workflows.** Implement D-008 while preserving correlation, middleware, retry, and uniqueness behavior.

Exit criteria:

- Optional drivers no longer depend on mutable global runtime hooks.
- The root public API is backed by one explicit composition path.
- Mixed supported module/envelope versions have documented and tested behavior.

## Milestone M3: Make Workflow Semantics Durable and Atomic

Objective: chains, batches, callbacks, and stores behave correctly across retries, concurrency, restarts, and multiple processes.

- [ ] **M3-01 — Implement the workflow durability contract.** Apply D-002 at construction/readiness time.
- [ ] **M3-02 — Replace durable closure callbacks.** Persist named continuation jobs or registered callback identifiers; keep closures only as explicit ephemeral compatibility behavior.
- [ ] **M3-03 — Add store lifecycle ownership.** Close internally opened SQL resources and never close caller-owned resources.
- [ ] **M3-04 — Make chain advancement atomic.** Use transactions, row locks, or compare-and-swap semantics and test concurrent duplicate deliveries.
- [ ] **M3-05 — Make batch aggregation atomic.** Prevent lost updates across simultaneous workers and drivers.
- [ ] **M3-06 — Recover partial dispatch.** Ensure a partially enqueued batch or chain reaches a recoverable terminal or resumable state rather than permanent pending state.
- [ ] **M3-07 — Make callback invocation truthful.** Mark callbacks complete only after a callback is found and succeeds or reaches a defined terminal outcome.
- [ ] **M3-08 — Unify memory and SQL store contracts.** Align missing-ID, terminal transition, clock, copy/ownership, and validation behavior.
- [ ] **M3-09 — Make schema migration configurable.** Preserve an explicit `AutoMigrate=false`, define migration ownership, and test transient migration failures and restart.
- [ ] **M3-10 — Reclassify the Temporal adapter.** Either implement a real external workflow-engine contract or clearly separate the current façade from queue-backed workflow guarantees.

Exit criteria:

- Durable workflows survive process restart and producer/worker separation.
- Duplicate deliveries cannot duplicate state transitions or callbacks.
- Store behavior is consistent and concurrency-tested on SQLite, MySQL, and PostgreSQL.

## Milestone M4: Converge Driver Guarantees

Objective: every advertised capability has a conformance test and every semantic difference is explicit.

- [ ] **M4-01 — Resolve NATS semantics.** Implement D-004 and test two workers, crash recovery, delayed work, broker restart, and poison handling.
- [ ] **M4-02 — Define poison/dead-letter behavior.** Prevent silent deletion/acknowledgement of malformed, unhandled, and terminally failed jobs in SQS and RabbitMQ.
- [ ] **M4-03 — Harden SQS delivery.** Configure visibility/redrive behavior, extend visibility for long handlers, validate credential pairs, and surface receive/delete failures.
- [ ] **M4-04 — Harden RabbitMQ delivery.** Add publisher confirms for dispatch/retry, worker reconnect, context-aware dialing, and safe settlement during drain.
- [ ] **M4-05 — Harden Redis resources and admin.** Close every owned client, align totals/windows, remove unreachable queue-resolution branches, and define bounded clear behavior.
- [ ] **M4-06 — Harden SQL execution and admin.** Surface finalization failures, constrain active-job admin races, check affected rows, preserve timeout precision, and index claim/recovery queries.
- [ ] **M4-07 — Make uniqueness claims precise.** Label each driver as process-local or distributed and test exactly that scope.
- [ ] **M4-08 — Separate guarantees from evidence.** Maintain a portable contract plus a driver evidence matrix whose cells link to executable scenarios.

Exit criteria:

- No driver is described as at-least-once unless acceptance, persistence, settlement, retry, and crash boundaries support that claim.
- Unsupported capabilities report false before invocation rather than failing after an optimistic support check.
- Multi-process and broker-fault scenarios cover every durable backend.

## Milestone M5: Unify Observability, Administration, and Test Doubles

Objective: operational surfaces describe real state consistently and testing APIs model production behavior.

- [x] **M5-01 — Define a shared root event envelope.** The normal facade now includes stable correlation, layer/source, queue, logical job, delivery attempt, event identity, and timestamps. Settlement-owner completion remains M1-04, and the legacy `bus` envelope remains M2-07.
- [ ] **M5-02 — Preserve distinct event vocabularies without duplicate observer models.** Transport and workflow events may differ, but subscription and correlation should be coherent.
- [x] **M5-03 — Correct event ordering.** Local acceptance callbacks and workerpool delivery gates ensure synchronous/in-process processing cannot begin before enqueue acceptance appears to observers; distributed arrival order remains correlation-based rather than globally ordered.
- [ ] **M5-04 — Make stats semantically comparable.** Define pending, scheduled, retry, active, processed, failed, and throughput windows for each capability level.
- [ ] **M5-05 — Make history instance-scoped and truthful.** Do not present process-wide sampled memory as durable backend history.
- [ ] **M5-06 — Consolidate admin APIs.** Remove `any`-based duplicate paths over time and align not-found, unsupported, and active-operation behavior.
- [ ] **M5-07 — Consolidate fakes.** Provide one concurrency-safe public fake that records dispatch at execution time and supports chain/batch assertions accurately.
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
| DL-007 | 2026-07-18 | Take an explicit pre-v1 observer compatibility boundary instead of retaining two typed models or using `any`. | Migration covers keyed event literals, custom bus observers, layer filtering, and concurrent observer calls; no persisted-data or wire change is implied. |
| DL-008 | 2026-07-18 | Invert orchestration dependencies through `internal/workflow`; root owns public models and `bus` becomes a compatibility facade. | Extract behavior first, preserve the legacy raw-runtime route, and migrate type ownership incrementally with compile and wire fixtures. |

## Progress Log

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
- Made permanent outcomes operational across local, Redis, SQL, NATS, SQS, and RabbitMQ workers, and introduced a distinct uncommitted outcome for infrastructure/workflow-state failures. Local, SQL, NATS, SQS, and RabbitMQ preserve that application attempt; Redis does so only before Asynq's final transport attempt, an explicit M1-04 blocker.
- Deferred logical job and chain/batch terminal facts until the owning workflow mutation commits. Chain, batch, and callback store failures now return the uncommitted outcome, suppress premature callbacks/events, preserve the store cause, and have exhausted-attempt recovery/idempotency regressions.
- Separated synchronous continuation failure from its predecessor's physical outcome: a downstream chain node can return its exact error to the caller without retrying the already-successful node or corrupting failed state into completion.
- Verified the reconciled branch with the full 12-module test-and-vet matrix, independent nested modules under `GOWORK=off`, root race tests, README snippet compilation, the module inventory guard, and stable README/example generation. Integration test-count discovery timed out at its bounded 30-second limit and deliberately retained the existing integration badge rather than fabricating a count.

## Next Action

Finish **M1-01/M1-04** by proving workflow-store and callback failures through settlement owners and resolving Asynq's exhausted-uncommitted limitation without breaking queued-task compatibility. Then complete **M1-05/M1-06** with one canonical logical uniqueness identity before beginning the behavior-preserving `internal/workflow` extraction from **M2-07**.
