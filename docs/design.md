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
  - `docs/integration-scenarios.md`

Notes:

- The codebase currently uses `Queue` terminology (no public `Dispatcher` API).
- Public `Worker` API currently has `Driver`, `Register`, `Start`, `Shutdown` (no `Pause`/`Resume` on `Worker` in this snapshot).
- Queue-level pause/resume helpers exist in observability helpers (`Pause`/`Resume`) and are driver-capability dependent.

## Product intent

Provide one stable queue API that can run across multiple backends without forcing app code changes.

Primary objectives:

- consistent dispatch/handler programming model
- explicit job metadata (retry/delay/timeout/queue/uniqueness)
- backend-specific durability/performance where available
- strong reliability and scenario coverage across supported drivers
- useful observability hooks independent of backend

## Public API model

## Queue API

`Queue` is the runtime abstraction:

- `StartWorkers(ctx context.Context) error`
- `Dispatch(job any) error`
- `WithContext(ctx).Dispatch(job)` for context-aware dispatch
- `Register(jobType string, handler Handler)`
- `Shutdown(ctx context.Context) error`

Constructor:

- `New(cfg Config) (Queue, error)`

`Config` contains shared and driver-specific constructor fields (`Driver`, database, Redis, NATS, SQS, RabbitMQ, and observer hook).

## Worker API

`Worker` remains an internal runtime wrapper used by queue implementations:

- `Driver() Driver`
- `Register(jobType string, handler Handler)`
- `StartWorkers(context.Context) error`
- `Shutdown() error`

Constructor path:

- `q, _ := New(cfg)`
- `q.Register(...)`
- `q.StartWorkers(ctx, n)`

Worker execution settings are configured from `Queue` via `Workers(n).StartWorkers(ctx)`; there is no separate public worker config surface.

## Job API (fluent value object)

`Job` is immutable-ish via value receivers and explicit execution boundary (`Queue.WithContext(ctx).Dispatch(...)`):

- `NewJob(jobType string) Job`
- `Payload(any) Job`
- `PayloadJSON(any) Job`
- `OnQueue(string) Job`
- `Timeout(time.Duration) Job`
- `Retry(int) Job`
- `Backoff(time.Duration) Job`
- `Delay(time.Duration) Job`
- `UniqueFor(time.Duration) Job`
- `PayloadBytes() []byte`
- `Bind(dst any) error`

Important behavior:

- Job contains data/metadata only.
- No `Job.Dispatch()` or hidden dispatch paths.
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
- Observed wrappers emit normalized events around dispatch and processing lifecycle.

## Observability design

Primary types:

- `EventKind` (dispatch/process/pause lifecycle constants)
- `Event` (kind, driver, queue, job type/key, retry/attempt, timing, error)
- `Observer` + `ObserverFunc`
- `StatsCollector`
- `StatsSnapshot`, `QueueCounters`, throughput windows

Helper APIs:

- `Pause(ctx, q, queueName)`
- `Resume(ctx, q, queueName)`
- `Snapshot(ctx, q, collector)`
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

- Queue-level pause helpers exist (`Pause` / `Resume`) and are only available where the queue driver implements control support.
- Worker-level pause controls are not part of the public `Worker` interface in this snapshot.

This distinction is important for UX/design decisions going forward.

## Reliability and scenario

Core scenario test:

- `TestIntegrationScenarios_AllBackends` in `integration_scenarios_test.go`

Named scenario coverage includes:

- lifecycle idempotency
- burst dispatch + completion
- poison/retry behavior
- restart recovery
- invalid bind payload handling
- uniqueness scope
- context-cancel dispatch behavior
- shutdown during delayed/retry load
- contention and idempotency patterns
- broker fault + recovery path
- ordering contract checks
- backpressure saturation
- large payload coverage
- config/option fuzz
- optional soak scenario (`RUN_SOAK=1`)

Roadmap doc:

- `docs/integration-scenarios.md` (baseline, planned extensions, execution model)

Observability integration contracts:

- `TestObservabilityIntegration_AllBackends`
- `TestObservabilityIntegration_PauseResumeSupport_AllBackends`

## CI/reliability gates

Primary CI workflow:

- `.github/workflows/test.yml`
  - unit tests
  - race tests
  - backend integration matrix
  - integration scenario matrix
  - scenario contract guard job

Guard script:

- `.github/scripts/check_integration_scenarios_contract.sh`
  - verifies required integration scenarios exist
  - verifies docs list baseline scenarios
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
- job options table
- reliability guarantees
- trust matrix
- observability quick guide
- logging hook guidance (`Observer`)
- autogenerated API reference block

Docs moved into `docs/` to keep root cleaner:

- `docs/events.md`
- `docs/integration-scenarios.md`
- `docs/laravel.md`
- `docs/readme-ref.md`

Examples:

- generated/compiled from doc comments via `example_compile_test.go`
- every example directory under `examples/` must build

## Design choices that matter

- No hard coupling to ORM layer for database driver behavior.
- Queue and worker remain separate constructs for clarity and backend parity.
- Job builder is fluent and data-only; execution side effects stay on queue/worker.
- Observability is additive and wrapper-based rather than per-driver bespoke APIs.
- Reliability posture is enforced through contract + scenario matrices, not ad-hoc tests.

## Known limitations / future work candidates

- Worker-level pause/resume ergonomics may still be desired as first-class API.
- Queue-level pause is backend-capability dependent.
- Some scenario extension items in `docs/integration-scenarios.md` are still planned.
- Long-run soak is scheduled/opt-in, not part of every PR run.

## Practical handoff checklist for next implementer

If continuing work, start here:

1. Read:
   - `README.md`
   - `docs/events.md`
   - `docs/integration-scenarios.md`
2. Validate baseline locally:
   - `go test ./...`
   - `INTEGRATION_BACKEND=all go test -tags=integration ./integration/...`
3. For reliability changes:
   - update integration scenarios in `integration_scenarios_test.go`
   - update `docs/integration-scenarios.md`
   - ensure `.github/scripts/check_integration_scenarios_contract.sh` still passes
4. For observability changes:
   - preserve event compatibility or document contract changes in `docs/events.md`
   - update integration observability tests

## Appendix: key files

Core API:

- `queue.go`
- `worker.go`
- `job.go`
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

- `integration_scenarios_test.go`
- `contract_test.go`
- `contract_integration_test.go`
- `worker_contract_test.go`
- `worker_contract_integration_test.go`
