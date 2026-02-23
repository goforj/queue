# Runbook: Stuck Workers / No Progress

## Trigger

- Worker liveness probe failing
- Queue depth rising but processed count flat
- No `process_succeeded` events for a sustained interval
- DB-backed queue shows stale `processing` rows

## Immediate checks

- Confirm workers are running and not crash-looping
- Check CPU/memory saturation and goroutine/thread exhaustion
- Check dependency reachability (DB/broker/external APIs)
- Check recent deploy/feature flag changes

## Triage commands (examples)

- Worker logs:
  - `kubectl logs <worker-pod> --since=15m`
- Process/thread state (platform-specific)
- For DB-backed runtimes: inspect stale `processing` rows and recovery events (`process_recovered`)

## Mitigation

- Restart stuck worker instances (after capturing logs/stack traces if possible)
- Scale out workers if saturation is the cause
- Roll back recent release if regression suspected
- For DB-backed runtimes, confirm stale-processing recovery is enabled/configured

## Verification

- Workers pass liveness/readiness checks
- Processing resumes and backlog drains
- `process_recovered` events return to baseline after recovery

## Follow-up

- Add/adjust worker liveness and progress alerts
- Capture traces/pprof during incidents if available
- Add regression test/chaos scenario for the discovered stuck condition
