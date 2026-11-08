# Temporal acceptance and protocol evidence

The optional driver uses Temporal SDK 1.45.0, API 1.62.12, gRPC 1.83.2 and the repository's Go 1.27 floor. The real-server image is CLI 1.8.0 / server 1.31.2, pinned by digest in `integration/testenv/temporal_image.go`. Module requirements and sibling replacements are inventoried by `scripts/check-module-inventory.sh`.

## Repeatable checks

```sh
go test -race ./...
GOWORK=off go -C driver/temporalqueue test -race ./...
INTEGRATION_BACKEND=temporal GOWORK=off go -C integration test -race -tags=integration ./temporal -count=1 -timeout=5m
TEMPORAL_PROOF=1 GOWORK=off go -C integration test -race -tags=integration ./temporalproof -count=1 -timeout=5m
INTEGRATION_BACKEND=sqlite,mysql,postgres GOWORK=off go -C integration test -race -tags=integration ./root -run '^TestNamedContinuations' -count=1 -timeout=3m
FULL=1 VET=1 ./scripts/test-all-modules.sh
```

The SDK intentionally retries nonretryable poll errors for about two minutes before invoking its fatal callback. The production fatal test exercises that actual path; its duration is expected. Production and feasibility tests are separate packages and produce distinct evidence.

The module inventory includes root, optional drivers, examples, integration and documentation tooling. Root `go test ./...` does not cross nested module boundaries. Minimum Go version, race matrix, coverage artifacts, sibling dependency versions and independent module publication all have explicit inventory checks. Root-only consumers retain a dependency graph without Temporal SDK or gRPC.

## Production evidence

| Area | Public resource evidence |
| --- | --- |
| Ordinary jobs | Canonical bytes, retries, middleware/observer outcome classification, correlation and missing-handler failures |
| Portable state | Accepted input before polling, committed state during work and retained terminal state through separate clients |
| Batch semantics | Failed aggregate plus late admitted member drain, allowed failures with successful SDK completion and every hook phase |
| Continuations | Stable IDs, safe immutable snapshots, declaration order, retry exhaustion and later completion hooks |
| Process loss | Independent producer/worker/reader processes, external effect before completion acknowledgement, and journal generation before task commit |
| Replay | Original production history, retained fixture, SDK command replay, regenerated delta equality and content-only negative control |
| Native workflows | Typed results, signals, queries, validated updates and update-with-start, timer, child, version marker and continue-as-new |
| Routing and capacity | Combined and split roles, explicit remote route, local activities, shared portable/native remote capacity |
| Lifecycle | Owned/borrowed resources, actual fatal-poller propagation, concurrent shutdown joins, cancellation-resistant callbacks and typed result encoding during grace |
| Start uncertainty | Successful service acceptance followed by lost response, original immutable input reconciliation, collision and inaccessible/expired history controls |
| Reset and isolation | Real administrative reset rejection, actual run checks at generic activity entry, and resource-bound authorized reads |
| Protection | Bounded authenticated fixture conversion through dispatch/hooks/reads/replay, sentinel checks in persisted history and SDK logs |
| Scale | 256-member fan-out with bounded history pages, 32 hook definitions and immediate over-limit/multiplication rejection |
| SQL recovery | SQLite/MySQL/PostgreSQL atomic state and intents, recovery without surviving business delivery, competing transitions, one-connection restart, managed schema and pruning retention |

The AEAD test fixture uses a deliberately public synthetic key, bounded bytes and no tenant AAD. It demonstrates the adapter boundary and must never be used to protect application data.

## Independent review fixes

Several independent protocol, security, runtime and recovery passes found and verified fixes for:

- Batch fail-fast cancellation of already admitted siblings, cancellation reported as successful closure, and failed business aggregates incorrectly closing successfully.
- Reset lineage and cross-resource payload reads, immutable member association and stable activity IDs.
- Malformed retained journal records, missing accepted definitions, and RPC diagnostics collapsing authorization, outage and absence into one category. Production-history mutation controls exercise the actual state reader.
- SDK Memo default-converter fallback after authoritative codec rejection, copied hook payloads in journal deltas, maximum journal-size reservation and partial transition sequencing.
- Dynamic credential/transport callback panic escape, ambiguous acceptance after a post-commit interceptor failure and lost uncertainty across retry responses.
- Publisher cancellation reaching accepted local hooks, synchronous hook panic escape, leaked cancellation-detach markers and silently ignored hook uniqueness.
- Publication/execution lease interference, retry-budget fencing, duplicate terminal hooks, single-connection SQL lock inversion and recovery-index validation.
- Transient SQL settlement failure repeating or stranding an effect, application timeout preventing durable settlement, and idempotent settlement accepting a stale or forged owner.
- Oversized broker claims reaching decoding, catalogs omitting JSON/base64 expansion and routes, and post-default route resolution bypassing the amplified ledger reservation. Both encoded catalog and worst-case footprint checks run after defaults resolve.
- Graceful worker shutdown closing serialization adapters before admitted native result encoding, with retryable availability preserved for calls arriving after SDK grace expires.

Review approval applies to the inspected code and checks. It does not replace required feature acceptance or establish an actual published module release.

## Generated composition and publication

Qualify generated projects under `/tmp`, including the default App and a named App, jobs, schedules, HTTP, metrics and independent queue resource names. Compile native workflow/activity definitions and portable continuation usage against the exact candidate root and optional module. Preserve application-owned registration through rerender. Record any unrelated baseline failure separately from queue-specific checks.

Publish root and each optional module using the repository release scripts and synchronized version policy after merge and release approval. Verify each module tag independently. Then validate a consumer with `GOWORK=off` and no local replacements against those actual published versions. Framework configuration rollout and generated Temporal service planning follow that published-module qualification.

The local qualification used the full generated default and `customer-portal` Apps at GoForj `5ab78874`, with the exact candidate queue modules selected through temporary replacements. All generated packages built. App/Wire, jobs, schedules, queues and the application-owned dual-resource/native-workflow fixture passed under the race detector. Queue regeneration preserved that fixture and generated manager bytes.

That framework revision still emits the retired `queue.Config.Observer` field. The isolated qualification applied the existing [legacy API migration](legacy-api-migration.md), moving that argument to `queue.WithObserver` for native queue constructors. This adaptation belongs in the downstream framework rollout. The unmodified framework's full render test command separately fails its HTTP maintenance response assertions with the old queue dependency as well; those baseline failures do not establish a queue regression or a passing full framework test suite.
