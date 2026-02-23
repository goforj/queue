# Runbook: Poison Messages / Retry Churn

## Trigger

- Retry rate alert fires
- Same job type/key repeatedly fails and retries
- Backlog grows while one/few job types dominate failures
- Archived/dead-letter behavior (where supported) increases

## Immediate checks

- Identify top failing job type(s) and queue(s)
- Inspect error signature (`last_error`) and retry attempts
- Determine if failure is data-specific (single payload) or systemic (dependency outage)

## Triage commands (examples)

- Search worker logs for repeated job key/type failures
- Query backend state (DB row / broker metadata / archived jobs if supported)

## Mitigation

- Fix underlying dependency/config issue if systemic
- Temporarily disable producer path for the failing job type
- Quarantine or skip known-bad payloads
- Lower concurrency for the failing queue if it is starving healthy jobs
- Requeue only after validating fix (avoid replay storms)

## Verification

- Retry rate declines
- Healthy jobs continue to complete
- Poison jobs stop cycling (archived/quarantined/handled)
- `scenario_poison_message_max_retry` invariant is preserved in production behavior (bounded retries)

## Follow-up

- Add payload validation earlier in producer path
- Improve handler error classification (retryable vs terminal)
- Add alerting on repeated failures per job type/key
