# Runbook: Backlog Growth

## Trigger

- Queue depth/backlog alert fires
- Processing latency grows while enqueue rate remains high
- `StatsSnapshot.Pending` / `StatsSnapshot.Scheduled` trend upward without recovery

## Immediate checks

- Confirm affected queue(s) and backend (`redis`, `mysql`, `postgres`, `rabbitmq`, `sqs`, etc.)
- Check worker liveness and recent deploys/restarts
- Check error/retry rate and `republish_failed` / `process_recovered` event spikes
- Check whether the backlog is mostly:
  - pending
  - scheduled/delayed
  - retries / poison candidates

## Triage commands (examples)

- App logs for worker errors/retries:
  - `kubectl logs <worker-pod> --since=15m`
- Integration-style local baseline:
  - `RUN_INTEGRATION=1 INTEGRATION_BACKEND=<backend> go test -tags=integration ./... -run '^TestIntegrationScenarios_AllBackends$' -count=1`

## Likely causes

- Worker fleet down or underprovisioned
- Handler latency spike / dependency outage
- Poison jobs causing retry churn
- Broker/database connectivity issues
- Misconfigured concurrency (`Workers(count)`) or queue routing mismatch

## Mitigation

- Scale workers for the affected queue(s)
- Pause non-critical queues if supported (`queue.Pause(...)`)
- Reduce enqueue rate upstream (backpressure / feature toggle)
- Quarantine poison job types (temporarily disable producer path or route elsewhere)
- Restart unhealthy workers after confirming no rollout issue

## Verification

- Queue depth trend reverses within expected recovery window
- Processing throughput increases and retry/failure rate drops
- No sustained `republish_failed` spikes

## Follow-up

- Capture root cause and timeline
- Add/adjust alert thresholds and dashboard panels
- Add scenario/soak coverage if the failure mode was new
