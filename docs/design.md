# Queue Library Design and Handoff

This document is a comprehensive snapshot/handoff of the `github.com/goforj/queue` codebase as it exists now.
It is intended for:

- humans onboarding to architecture and reliability expectations
- future agents continuing implementation without re-discovery

## Snapshot (current workspace)

Captured from current workspace state before writing this file:

- `git status --short`:
  - `M CONTRIBUTING.md`
- Key docs currently in `docs/`:
  - `docs/events.md`
  - `docs/hardening.md`

Notes:

- The codebase currently uses `Queue` terminology (no public `Dispatcher` API).
- Public `Worker` API currently has `Driver`, `Register`, `Start`, `Shutdown` (no `Pause`/`Resume` on `Worker` in this snapshot).
- Queue-level pause/resume helpers exist in observability helpers (`PauseQueue`/`ResumeQueue`) and are driver-capability dependent.

## Product intent

Provide one stable queue API that can run across multiple backends without forcing app code changes.

Primary objectives:

- consistent enqueue/handler programming model
- explicit task metadata (retry/delay/timeout/queue/uniqueness)
- backend-specific durability/performance where available
- strong reliability and hardening coverage across supported drivers
- useful observability hooks independent of backend

## Public API model

## Queue API

`Queue` is the runtime abstraction:

- `Start(ctx context.Context) error`
- `Enqueue(ctx context.Context, task Task) error`
- `Register(taskType string, handler Handler)`
- `Shutdown(ctx context.Context) error`

Constructor:

- `New(cfg Config) (Queue, error)`

`Config` contains shared and driver-specific constructor fields (`Driver`, database, Redis, NATS, SQS, RabbitMQ, and observer hook).

## Worker API

`Worker` is separate from `Queue` for backends where producer/consumer lifecycle are split:

- `Driver() Driver`
- `Register(taskType string, handler Handler)`
- `Start() error`
- `Shutdown() error`

Constructor:

- `NewWorker(cfg WorkerConfig) (Worker, error)`

`WorkerConfig` is intentionally separate from `Config` so execution-side knobs and enqueue-side knobs are documented independently.

## Task API (fluent value object)

`Task` is immutable-ish via value receivers and explicit execution boundary (`Queue.Enqueue`):

- `NewTask(taskType string) Task`
- `Payload(any) Task`
- `PayloadJSON(any) Task`
- `OnQueue(string) Task`
- `Timeout(time.Duration) Task`
- `Retry(int) Task`
- `Backoff(time.Duration) Task`
- `Delay(time.Duration) Task`
- `UniqueFor(time.Duration) Task`
- `PayloadBytes() []byte`
- `Bind(dst any) error`

Important behavior:

- Task contains data/metadata only.
- No `Task.Enqueue()` or hidden dispatch paths.
- `Bind` enforces pointer destination and uses JSON unmarshal.

## Driver architecture

Supported drivers:

- `sync` (inline execution)
- `workerpool` (in-process async)
- `database` (`sqlite`, `mysql`, `postgres/pgx`)
- `redis` (Asynq-backed)
- `nats`
- `sqs`
- `rabbitmq`

Execution model summary:

- `sync` and `workerpool` are local runtimes.
- `database` persists jobs in SQL and polls/claims work.
- `redis` uses Asynq queue + separate worker server.
- `nats`/`sqs`/`rabbitmq` each have producer + worker implementations with retry/delay mapping behavior.

Implementation detail:

- Constructors wrap queue/worker with observed wrappers when `Observer` is configured.
- Observed wrappers emit normalized events around enqueue and processing lifecycle.

## Observability design

Primary types:

- `EventKind` (enqueue/process/pause lifecycle constants)
- `Event` (kind, driver, queue, task type/key, retry/attempt, timing, error)
- `Observer` + `ObserverFunc`
- `StatsCollector`
- `StatsSnapshot`, `QueueCounters`, throughput windows

Helper APIs:

- `PauseQueue(ctx, q, queueName)`
- `ResumeQueue(ctx, q, queueName)`
- `SnapshotQueue(ctx, q, collector)`
- `SupportsPause(q Queue) bool`
- `SupportsNativeStats(q Queue) bool`
- `MultiObserver(...)`
- `ChannelObserver`

Contract docs:

- `docs/events.md` defines event semantics and guarantees.

Safety:

- observer dispatch is panic-shielded (`safeObserve`) so observer panics do not break queue processing.

## Queue vs worker pause

In this snapshot:

- Queue-level pause helpers exist (`PauseQueue` / `ResumeQueue`) and are only available where the queue driver implements control support.
- Worker-level pause controls are not part of the public `Worker` interface in this snapshot.

This distinction is important for UX/design decisions going forward.

## Reliability and hardening

Core hardening test:

- `TestIntegrationHardening_AllBackends` in `integration_backends_integration_test.go`

Named step coverage includes:

- lifecycle idempotency
- burst enqueue + completion
- poison/retry behavior
- restart recovery
- invalid bind payload handling
- uniqueness scope
- context-cancel enqueue behavior
- shutdown during delayed/retry load
- contention and idempotency patterns
- broker fault + recovery path
- ordering contract checks
- backpressure saturation
- large payload coverage
- config/option fuzz
- optional soak step (`RUN_SOAK=1`)

Roadmap doc:

- `docs/hardening.md` (baseline, planned extensions, execution model)

Observability integration contracts:

- `TestObservabilityIntegration_AllBackends`
- `TestObservabilityIntegration_PauseResumeSupport_AllBackends`

## CI/reliability gates

Primary CI workflow:

- `.github/workflows/test.yml`
  - unit tests
  - race tests
  - backend integration matrix
  - integration hardening matrix
  - hardening contract guard job

Guard script:

- `.github/scripts/check_hardening_contract.sh`
  - verifies required hardening steps exist
  - verifies docs list baseline steps
  - verifies backend coverage references in integration contract suites
  - verifies required observability integration tests exist
  - supports `rg` fallback to `grep`
  - resolves repo root by script path (cwd-independent)

Nightly reliability workflow:

- `.github/workflows/soak.yml`
  - soak matrix (`RUN_SOAK=1`)
  - chaos subset matrix

## Documentation system

README has:

- quick start and driver usage
- task options table
- reliability guarantees
- trust matrix
- observability quick guide
- logging hook guidance (`Observer`)
- autogenerated API reference block

Docs moved into `docs/` to keep root cleaner:

- `docs/events.md`
- `docs/hardening.md`
- `docs/laravel.md`
- `docs/readme-ref.md`

Examples:

- generated/compiled from doc comments via `example_compile_test.go`
- every example directory under `examples/` must build

## Design choices that matter

- No hard coupling to ORM layer for database driver behavior.
- Queue and worker remain separate constructs for clarity and backend parity.
- Task builder is fluent and data-only; execution side effects stay on queue/worker.
- Observability is additive and wrapper-based rather than per-driver bespoke APIs.
- Reliability posture is enforced through contract + hardening matrices, not ad-hoc tests.

## Known limitations / future work candidates

- Worker-level pause/resume ergonomics may still be desired as first-class API.
- Queue-level pause is backend-capability dependent.
- Some hardening extension items in `docs/hardening.md` are still planned.
- Long-run soak is scheduled/opt-in, not part of every PR run.

## Practical handoff checklist for next implementer

If continuing work, start here:

1. Read:
   - `README.md`
   - `docs/events.md`
   - `docs/hardening.md`
2. Validate baseline locally:
   - `go test ./...`
   - `RUN_INTEGRATION=1 go test -tags integration ./...`
3. For reliability changes:
   - update hardening steps in `integration_backends_integration_test.go`
   - update `docs/hardening.md`
   - ensure `.github/scripts/check_hardening_contract.sh` still passes
4. For observability changes:
   - preserve event compatibility or document contract changes in `docs/events.md`
   - update integration observability tests

## Appendix: key files

Core API:

- `queue.go`
- `worker.go`
- `task.go`
- `driver.go`

Drivers:

- `queue_local.go`
- `queue_database.go`
- `queue_redis.go`
- `queue_nats.go`
- `queue_sqs.go`
- `queue_rabbitmq.go`
- `worker_redis.go`
- `worker_nats.go`
- `worker_sqs.go`
- `worker_rabbitmq.go`

Observability:

- `observability.go`
- `observability_test.go`
- `observability_integration_test.go`
- `observability_benchmark_test.go`

Reliability/integration:

- `integration_backends_integration_test.go`
- `contract_test.go`
- `contract_integration_test.go`
- `worker_contract_test.go`
- `worker_contract_integration_test.go`
