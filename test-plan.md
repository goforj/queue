# Test Plan (Guarantee-First, V1 Readiness)

This plan is intentionally not coverage-first. For a queue/workflow library, trust comes from proving behavioral guarantees across drivers under normal load, contention, and failure.

Coverage remains a useful regression signal, but it is secondary to cross-driver scenario validation.

## Principles

- Test guarantees, not internal implementations.
- Run the same scenario names across all supported drivers whenever possible.
- Capability-gate only where a backend truly cannot provide a guarantee.
- Document every unsupported or weaker guarantee explicitly.
- Treat flaky tests as product issues until proven infra-only.

## What “Trusted” Means for This Library

Before `v1.0.0`, users should be able to rely on:

- Clear delivery and retry semantics (including duplicate-delivery expectations)
- Documented ordering guarantees (and non-guarantees) per driver
- Safe worker lifecycle behavior (`StartWorkers`, `Shutdown`) under repeated calls and races
- Predictable workflow behavior (chain/batch state transitions, callbacks, terminal states)
- Capability-consistent behavior for pause/stats/observability
- Recovery behavior across restart/outage scenarios where supported

## Current Test System (What We Already Do)

## 1. Root/unit/package tests

Primary command:

```bash
GOCACHE=/tmp/queue-gocache go test ./...
```

What this currently gives us:

- Core queue API behavior in the root module
- `bus` workflow runtime/store semantics and callback idempotency tests
- Fake queue behavior used by application tests
- Internal bridge regressions (`internal/driverbridge`)
- API-level behavior and option handling

## 2. Multi-module sweep (root + submodules)

Script:

```bash
./scripts/test-all-modules.sh
```

Modes:

- default: compile-only (`-run '^$'`)
- `FULL=1`: full tests per module

What this gives us:

- cross-module build integrity (`GOWORK=off` in submodules)
- optional driver modules still compile/test independently
- examples and integration module stay wired correctly

## 3. Integration suite (cross-driver scenarios)

Primary command:

```bash
INTEGRATION_BACKEND=all GOCACHE=/tmp/queue-gocache go test -tags=integration ./integration/... -count=1
```

Current integration coverage is already strong and includes:

- shared queue-runtime scenarios in `integration/all/integration_scenarios_test.go`
- queue/workflow API integration in `integration/all/runtime_integration_test.go`
- bus integration in `integration/bus/integration_test.go`
- observability integration in `integration/root/observability_integration_test.go`
- SQL callback duplicate-suppression integration tests in `integration/bus/callback_sql_integration_test.go`

Related documentation and CI checks:

- `docs/integration-scenarios.md`
- `.github/scripts/check_integration_scenarios_contract.sh`

## 4. Coverage aggregation (supporting signal)

Script:

```bash
scripts/coverage-codecov.sh
```

Current role:

- merges unit + integration-tagged coverage
- tracks broad regressions
- not used as a substitute for guarantee validation

## 5. Docs/examples validation

Commands used during API changes:

```bash
GOCACHE=/tmp/queue-gocache go run ./docs/readme/main.go
GOCACHE=/tmp/queue-gocache go run ./docs/examplegen/main.go
cd examples && GOCACHE=/tmp/queue-gocache go test ./... -run '^TestExamplesBuild$' -count=1
```

What this gives us:

- generated docs/examples remain in sync
- example code compiles

Gap still open:

- manual README snippets are not automatically compile-checked

## Behavioral Guarantee Areas (V1)

This section is the core of the plan. Each area should have:

- a documented guarantee (or explicit non-guarantee)
- shared scenario coverage (where possible)
- capability gates only where unavoidable

## A. Delivery and Acknowledgement Semantics

### V1 guarantees to document and enforce

- Delivery is at-least-once (if that is the intended contract)
- Duplicate delivery is possible and callers must design handlers accordingly
- A job is not considered complete until handler success is acknowledged by the backend/runtime path
- Handler error behavior maps to retry/archive semantics as documented

### Covered today (good)

- poison message and max retry handling
- duplicate-delivery idempotency scenario
- restart recovery scenarios
- broker fault / recovery scenarios (capability-gated)

### Gaps to add

- Explicit ack-boundary invariants under worker interruption
  - example: handler side effect committed, ack path interrupted, duplicate delivery occurs; verify idempotency pattern and state consistency
- “success exactly once” is not promised; test and document “side-effect idempotency required” with reference scenario
- Delayed job + restart + recovery invariants for all backends that claim durable delay/retry behavior

## B. Retry, Delay, and Scheduling Semantics

### V1 guarantees to document and enforce

- Retry count increments correctly
- Retry exhaustion leads to documented terminal behavior
- Delay/retry scheduling does not execute before configured time window (allow backend timing tolerance)
- Backoff strategy is respected within documented tolerance, not exact timestamps

### Covered today (good)

- poison-message retry ceiling
- shutdown during delay/retry
- mixed option fuzz scenarios
- backpressure and delayed workloads in baseline suite

### Gaps to add

- Timing-window assertions for delay/backoff (early execution is a correctness bug)
- Backoff monotonicity under load (retry N+1 should not run before retry N for same job path)
- Deadline/timeout interaction with retry policy (especially per-driver timeout implementations)
- Jitter behavior (if supported/documented) should be tested as range-based, not exact

## C. Ordering Semantics (Most Common Source of User Misunderstanding)

### V1 guarantees to document and enforce

Per driver, explicitly state one of:

- no ordering guarantee
- best-effort FIFO per queue
- FIFO only under constrained conditions (single worker, no retry, no delay, no priority)

### Covered today (good)

- `scenario_ordering_contract` with capability gating

### Gaps to add

- Split ordering scenarios by condition, not one broad “ordering”
  - single worker / no retry
  - multi-worker contention
  - retries injected (ordering should be allowed to break if documented)
  - delayed + immediate mix
- Assert documented non-guarantees
  - example: prove ordering may break under multi-worker concurrency for drivers where only best-effort ordering exists
- Add a driver-facing matrix in docs that names exact ordering conditions

## D. Worker Lifecycle and Shutdown Semantics

### V1 guarantees to document and enforce

- `StartWorkers` is safe/idempotent
- `Shutdown` is safe/idempotent
- Shutdown behavior for in-flight jobs is documented
  - graceful completion, interruption, retry rescheduling, or handoff behavior

### Covered today (good)

- `scenario_startworkers_idempotent`
- `scenario_shutdown_idempotent`
- `scenario_shutdown_during_delay_retry`
- worker restart recovery scenarios

### Gaps to add

- Shutdown during active handler execution with long-running jobs
  - verify resulting job state and retry behavior
- Start/shutdown race stress (rapid worker joins/leaves)
- Repeated startup/shutdown cycles on same queue under light load (resource leak smoke)

## E. Dispatch Semantics (`Dispatch` / `DispatchCtx`)

### V1 guarantees to document and enforce

- `Dispatch(job)` uses default background context behavior
- `DispatchCtx(ctx, job)` obeys cancellation/deadline for enqueue operation
- Context cancellation should not enqueue the job if cancellation occurs before acceptance (within documented backend tolerance)

### Covered today (good)

- `scenario_dispatch_context_cancellation`
- dispatch burst and dispatch under broker fault scenarios

### Gaps to add

- Distinguish pre-canceled vs mid-flight canceled contexts
- Deadline exceeded error shape consistency (or documented backend-specific error wrapping)
- Queue-level defaults vs per-call context interaction (if queue-level timeouts/controls are configurable)

## F. Workflow Semantics (Chain/Batch/Callbacks)

This is a trust-critical area. Users will assume high-level workflow helpers encode strong invariants.

### V1 guarantees to document and enforce

- Chain steps advance only after prior step success
- Chain failure triggers catch/finally exactly once per workflow (under documented duplicate-delivery assumptions)
- Batch terminal state reflects child outcomes correctly
- Batch `then`/`catch`/`finally` callbacks run according to documented rules and are duplicate-safe
- Workflow state lookup/prune behavior is consistent and documented

### Covered today (good)

- root `bus` tests cover chain/batch lifecycle and callback de-duplication
- SQL store contract tests cover callback marker/idempotency behavior
- integration queue and bus tests cover chain/batch end-to-end scenarios
- SQL runtime callback duplicate suppression integration tests exist

### Gaps to add

- Fault-injected workflow callback delivery duplication across more backends (where callback path traverses queue runtime)
- Callback failure retry semantics (if callbacks retry / fail terminally, document and test)
- Workflow + queue outage interactions
  - enqueue succeeds for workflow record but queue dispatch fails (and recovery semantics)
- Multi-worker concurrent workflow progression stress
  - ensure no double-advance / double-terminal transitions

## G. Observability and Capability Contracts

### V1 guarantees to document and enforce

- `SupportsPause` / `Pause` / `Resume` behavior is correct and capability-gated
- `SupportsNativeStats` / `Stats` behavior is correct and capability-gated
- Observer event emission for key lifecycle transitions is stable enough for docs/contracts

### Covered today (good)

- observability integration suite
- pause/resume support integration checks
- internal bridge tests now guard stats/pause passthrough regressions

### Gaps to add

- Event ordering/sequence assertions for critical flows (dispatch -> start -> retry/fail -> success/archive)
  - allow tolerance where async delivery makes exact global ordering impossible
- Error-path observability assertions (broker fault, dispatch cancellation, callback failure)
- “no false support claims” checks across wrappers/adapters (this regressed once; keep pressure on it)

## H. Backpressure, Capacity, and Large Payload Behavior

### V1 guarantees to document and enforce

- System maintains forward progress under bounded saturation
- Backpressure errors/blocking behavior is documented (if applicable)
- Large payload handling limits and error behavior are documented

### Covered today (good)

- `scenario_backpressure_saturation`
- `scenario_payload_large`

### Gaps to add

- Explicit size limit boundary tests (just below / at / just above limit if limits are known)
- Recovery after saturation (normal traffic resumes cleanly)
- Queue depth / latency threshold assertions (range-based, backend-tolerant)

## I. Configuration and Validation Robustness

### Covered today (good)

- `scenario_config_option_fuzz`
- invalid JSON bind scenarios

### Gaps to add

- Invalid option combinations with explicit error shape assertions
- Fuzz/property tests for payload decoding and queue-name normalization
- Config defaults invariants (documented defaults should be test-locked)

## Test Types We Should Add or Expand (Prioritized)

## P0 (Before v1 tag)

## 1. Guarantee matrix doc (driver x guarantee)

- [x] Create/expand a driver guarantee matrix and link every guarantee to a proving test/scenario
- Location:
  - `docs/backend-guarantees.md` (or new dedicated matrix section/file)
  - `docs/integration-scenarios.md`
- Acceptance:
  - every supported backend row includes: ordering, pause/resume, native stats, restart recovery, broker fault injection support, delay/retry durability
  - every capability/guarantee entry points to at least one scenario/test name
  - all “unknown” cells are resolved to supported / unsupported / best-effort
- Notes:
  - capability-gated skips in integration tests must match the matrix

Create or expand a table that maps each driver to guarantee strength for:

- ordering
- pause/resume
- native stats
- restart recovery
- broker fault injection support
- delay/retry durability

Each row must point to scenario names/tests that prove it.

Why:

- This closes the gap between “we think this driver supports X” and “we can prove X in CI”

## 2. Ordering scenario split + documentation alignment

- [x] Split ordering coverage into condition-specific scenarios and align docs with exact guarantees
- Location:
  - `integration/all/integration_scenarios_test.go`
  - `docs/integration-scenarios.md`
  - `docs/backend-guarantees.md`
- Acceptance:
  - add scenarios (names can vary, but should be explicit) for:
    - single-worker FIFO
    - multi-worker non-FIFO / best-effort behavior
    - retry-induced reordering
    - delayed + immediate mix ordering behavior
  - each scenario is capability-gated only where needed
  - docs state exact ordering preconditions per backend (workers/retries/delay constraints)
- Notes:
  - include at least one “non-guarantee” assertion to prevent accidental over-promising

Refactor `scenario_ordering_contract` into sub-scenarios with explicit preconditions:

- single worker FIFO
- multi-worker no FIFO guarantee
- retry-induced reorder allowed
- delayed/immediate mix

Why:

- Ordering bugs and over-promises are the biggest trust killers in queue systems

## 3. Retry/delay timing-window assertions

- [x] Add tolerance-based timing-window assertions for delay/retry/backoff behavior
- Location:
  - `integration/all/integration_scenarios_test.go`
  - `docs/integration-scenarios.md` (timing tolerance documentation)
- Acceptance:
  - delayed jobs are asserted not to execute before the configured delay window (with backend tolerance)
  - retries are asserted not to execute before the expected backoff window (with backend tolerance)
  - late execution is tolerated within documented limits; early execution fails
  - thresholds are configurable similar to existing scenario duration guardrails
- Notes:
  - use range assertions; do not assert exact timestamps

Add tolerance-based assertions that jobs do not execute earlier than configured delay/backoff windows.

Why:

- “Runs too early” is a correctness bug; “runs a bit late” is usually capacity/timing

## 4. Workflow fault and duplicate callback integration expansion

- [x] Expand workflow integration coverage for callback duplication/failure/recovery semantics
- Location:
  - `integration/all/runtime_integration_test.go`
  - `integration/bus/integration_test.go`
  - `integration/bus/callback_sql_integration_test.go` (extend or mirror patterns)
  - workflow docs (`README.md` / workflow docs if applicable)
- Acceptance:
  - callback duplicate delivery is tested under at least one fault/recovery path
  - callback failure behavior (retry/terminal/catch/finally semantics) is explicitly asserted and documented
  - workflow state remains consistent after partial dispatch failures (workflow record + queue enqueue mismatch paths)
  - no double-advance / double-terminal transition under concurrent processing in covered scenarios
- Notes:
  - this is a trust-critical P0 item, not optional polish
  - Progress: cross-backend callback failure semantics (catch/finally + terminal state) are covered in `integration/bus/integration_test.go`; SQL runtime/store integration now covers chain + batch duplicate callback suppression, callback replay after callback-dispatch fault (chain final callback), and chain/batch dispatch failure state consistency (including batch partial-dispatch-failure-after-progress)

Extend workflow integration scenarios to cover:

- callback duplicate delivery under queue/runtime faults
- callback failure and retry/terminal semantics
- workflow state consistency after dispatch partial failures

Why:

- Workflow helpers amplify queue semantics; trust here matters more than raw queue enqueue tests

## 5. Race detector in CI (required)

- [x] Add race detection to CI release gate
- Location:
  - CI workflow(s) in `.github/workflows/*`
  - `test-plan.md` / release gate docs if split out
- Acceptance:
  - PR or required CI runs `go test -race` on an agreed scope
  - nightly or scheduled CI runs full `go test -race ./...` if PR scope is reduced
  - failures are triaged as product bugs unless proven infrastructure/tooling issues
- Notes:
  - Current PR CI scope is the root module race run in `.github/workflows/test.yml` (`race` job: `go test -race ./...`)
  - Integration-tagged race coverage is not in the PR gate and should be treated as future hardening if needed

At least:

```bash
GOCACHE=/tmp/queue-gocache go test -race ./...
```

If too slow:

- required subset in PR CI
- full repo race in nightly

## 6. README manual snippet verifier

- [x] Add automated compile-checking for selected manual `README.md` Go snippets
- Location:
  - new script/tool (e.g. `scripts/check-readme-snippets.sh` or `tools/readmecheck`)
  - CI workflow step
- Acceptance:
  - curated manual snippets are extracted or mirrored and compile-checked in CI
  - the `Dispatch`/handler-signature drift class is caught automatically
  - failures report which snippet section broke
- Notes:
  - start curated; full Markdown fence extraction can come later

Automate compile-checking of selected manual snippets.

Why:

- v1 trust includes accurate docs

## P1 (Strongly recommended pre-v1 or immediately after v1)

## 7. Adversarial failure injection suite (“chaos-lite”)

- [x] Expand scheduled adversarial integration scenarios for backend flaps and worker churn
- Location:
  - `integration/all/integration_scenarios_test.go`
  - `docs/integration-scenarios.md` (`chaos` section)
  - CI scheduled workflow(s)
- Acceptance:
  - scenarios cover broker disconnect during handler execution, reconnect/redelivery, and dispatch during backend flap (where supported)
  - worker restart churn under load is exercised on restart-capable backends
  - scenario results are visible in CI artifacts/logs with backend + scenario naming
- Notes:
  - capability-gate unsupported fault injection paths explicitly
  - Implemented in `.github/workflows/soak.yml` `integration-chaos` subset with shared scenario names aligned to current suite (`scenario_dispatch_during_broker_fault`, `scenario_consume_after_broker_recovery`, `scenario_worker_restart_recovery`, `scenario_worker_restart_delay_recovery`, plus contention/shutdown race probes); results are emitted with backend+scenario duration lines and uploaded per-backend logs

Expand scheduled integration scenarios for:

- broker disconnect during handler execution
- reconnect and redelivery behavior
- dispatch while backend is flapping
- worker restart churn under load

Why:

- Real production incidents happen in these edges, not in happy paths

## 8. Repeat-run / soak on timing-sensitive scenarios

- [x] Add repeated-run soak gate for timing/concurrency-sensitive scenarios with flake tracking
- Location:
  - CI scheduled workflow(s) / release-candidate workflow
  - `docs/integration-scenarios.md`
  - flake log doc (new)
- Acceptance:
  - selected scenarios run repeatedly per backend (or backend subsets)
  - flake rate is recorded by backend/scenario
  - release candidates require manual review of recent flake results
- Notes:
  - focus on contention, retry timing, shutdown races, ordering
  - Implemented via `.github/workflows/soak.yml` `integration-flake-repeat` (scheduled + manual) using `scripts/integration-flake-repeat.sh`; current backend subset is `redis`, `rabbitmq`, `sqs` with per-scenario flake-rate summaries/artifacts in `docs/flake-log.md` review format

Run critical scenarios repeatedly (nightly/RC gate):

- multi-worker contention
- duplicate-delivery idempotency
- shutdown during delay/retry
- ordering sub-scenarios

Track flake rates by backend/scenario.

## 9. Error contract tests

- [x] Add explicit error contract tests for user-facing error classes
- Location:
  - root tests (`*_test.go`)
  - `integration/all` for backend/dispatch errors
  - `integration/root` / `integration/bus` for workflow/state errors
- Acceptance:
  - invalid config errors are asserted (not just non-nil)
  - unsupported capability operations have stable/documented error behavior
  - `DispatchCtx` cancellation/deadline errors are asserted by class/message contract
  - workflow not-found / invalid-state errors are covered
- Notes:
  - avoid overfitting exact wrapped error strings unless intentionally part of API
  - Implemented across root + integration suites: root-level `error_contract_test.go` covers high-level `Queue.DispatchCtx` cancellation/deadline classes (deterministic saturation), unsupported capability errors for `Queue.Pause`, `Queue.Resume`, `Queue.Stats`, `ErrWorkflowNotFound` wrappers for `FindChain`/`FindBatch`, constructor guidance errors (unsupported/moved drivers), runtime dispatch input validation (`nil` job / uninferable job type), high-level dispatch validation (`nil` receiver / invalid job), and workflow builder invalid-state errors; shared integration scenarios validate backend `DispatchCtx` cancellation classes (`scenario_dispatch_context_cancellation`), and root/integration-root contract suites assert stable error-shape semantics for missing job type / missing handler and unsupported `Snapshot(...)` fallback behavior

Add assertions for user-visible errors:

- invalid config
- unsupported capability operations
- context deadline/cancel during dispatch
- workflow not found / invalid state transitions

Why:

- “Trusted” also means errors are actionable and stable enough to debug

## P2 (Post-v1 hardening)

## 10. Fuzz/property suites

- [x] Add fuzz/property tests for decoding, naming, and option-validation boundaries
- Location:
  - root package tests (`Fuzz...`)
  - `bus` package where parsing/validation applies
- Acceptance:
  - at least one fuzz target for payload binding/decoding
  - at least one fuzz/property target for queue-name normalization/validation
  - corpus seeds include malformed/edge payloads observed in integration tests
- Notes:
  - Implemented root fuzz/property targets in `fuzz_queue_test.go`: `FuzzJobBindMatchesJSONUnmarshal` (payload decode parity with `json.Unmarshal`, including malformed seeds) and `FuzzNormalizeQueueName` (empty->`default`, non-empty unchanged property)
  - keep runtime bounded for CI; run deeper fuzzing manually/nightly

Targets:

- payload bind/decode paths
- queue naming normalization/validation
- option parsing and combination validation

## 11. Performance regression guardrails

- [ ] Define and automate lightweight performance smoke thresholds
- Location:
  - benchmark/smoke tooling (existing docs/bench if reused)
  - CI scheduled or release-candidate workflow
- Acceptance:
  - guardrail checks exist for enqueue throughput and worker lifecycle latency
  - thresholds are documented as regression alarms, not optimization goals
  - release process includes checking these results
- Notes:
  - avoid flaky microbench gating in PR CI

Add smoke thresholds (not optimization benchmarks):

- enqueue throughput sanity
- worker lifecycle latency sanity
- no catastrophic regressions between releases

## 12. Versioned compatibility matrix automation

- [ ] Make tested backend versions explicit per release
- Location:
  - `docs/compatibility-policy.md`
  - CI config / release docs
- Acceptance:
  - supported backend versions are listed
  - tested versions used in CI are visible and tied to release notes
  - capability differences are documented for supported versions where relevant
- Notes:
  - users need to know what combinations are actually exercised

Track/test supported backend versions per release so users know what combinations are actually exercised.

## Release Gate (V1 Candidate)

Minimum gate for a v1 release candidate:

1. `GOCACHE=/tmp/queue-gocache go test ./...`
2. `GOCACHE=/tmp/queue-gocache ./scripts/test-all-modules.sh`
3. `GOCACHE=/tmp/queue-gocache FULL=1 ./scripts/test-all-modules.sh` (or equivalent split full runs)
4. `INTEGRATION_BACKEND=all GOCACHE=/tmp/queue-gocache go test -tags=integration ./integration/... -count=1`
5. `scripts/coverage-codecov.sh`
6. `GOCACHE=/tmp/queue-gocache go run ./docs/readme/main.go`
7. `GOCACHE=/tmp/queue-gocache go run ./docs/examplegen/main.go`
8. `cd examples && GOCACHE=/tmp/queue-gocache go test ./... -run '^TestExamplesBuild$' -count=1`
9. `.github/scripts/check_integration_scenarios_contract.sh`
10. `GOCACHE=/tmp/queue-gocache go test -race ./...` (or approved split race jobs)
11. One repeated-run integration pass (nightly or RC) on timing-sensitive scenarios with flake review

## Execution Backlog (Actionable Tracking)

Use this section for active implementation tracking. Move items from here to completed notes as they land.

## P0 Active

- [x] Driver guarantee matrix linked to tests/docs
- [x] Ordering scenarios split + docs alignment
- [x] Retry/delay timing-window assertions
- [x] Workflow fault + duplicate callback integration expansion
- [x] CI race job added (required scope defined)
- [x] README manual snippet verifier in CI

## P1 Active

- [x] Chaos-lite failure injection expansion
- [x] Repeat-run/soak flake tracking pipeline
- [x] Error contract tests

## P2 Active

- [x] Fuzz/property suites
- [ ] Performance regression guardrails
- [ ] Versioned compatibility matrix automation

## Maintenance Rules

- When adding a backend:
  - add it to the guarantee matrix
  - add/adjust capability gates
  - run/update shared integration scenarios
- When changing semantics:
  - update docs + tests in the same PR
  - state whether the guarantee strengthened, weakened, or was clarified
- When a regression escapes:
  - add a scenario/test entry for the missing guarantee class
  - record the incident in the flake/regression log
