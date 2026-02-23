# Runbook: Broker Outage / Broker Recovery

Applies to broker-backed runtimes such as `redis`, `nats`, `rabbitmq`, and `sqs` (or their local test equivalents).

## Trigger

- Broker health check fails
- Connection/dial errors spike in worker logs
- `scenario_dispatch_during_broker_fault`-like behavior observed in production (dispatch failures during outage)

## Immediate checks

- Confirm scope: one broker/node/region vs full outage
- Confirm affected backends and queues
- Check producer error rate vs consumer error rate
- Check whether jobs are failing fast (good) vs silently disappearing (bad)

## Triage commands (examples)

- Broker/container status:
  - `docker ps`
  - platform-specific broker health command
- Worker logs:
  - `kubectl logs <worker-pod> --since=15m`

## Expected behavior

- Dispatch attempts may fail/surface errors during outage
- Workers should reconnect/recover after broker returns
- Jobs should resume processing after recovery (at-least-once semantics)

## Mitigation

- Restore broker service first (network, auth, broker process, storage)
- Restart workers only if reconnect does not happen automatically after service recovery
- Avoid mass retries from producers if that worsens outage pressure

## Verification

- Broker health restored
- New dispatches succeed
- Consumers resume processing queued work
- Backlog begins draining
- Recovery/failure events stabilize

## Follow-up

- Record outage duration and message loss/duplication impact (if any)
- Add/adjust reconnect telemetry and alerts
- Validate `consume_after_broker_recovery` behavior in integration/chaos suite if needed
