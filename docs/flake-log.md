# Integration Flake Review Log

Use this document to review and record timing/concurrency flake evidence from the scheduled reliability workflow (`.github/workflows/soak.yml`), especially the `integration-flake-repeat` job.

## Why this exists

- Repeated-run failures are often timing-sensitive and may not reproduce in a single integration pass.
- For v1 trust, release candidates should be reviewed against recent flake evidence instead of relying only on point-in-time green runs.

## Source of truth

- CI workflow: `.github/workflows/soak.yml`
- Job: `integration-flake-repeat`
- Artifacts:
  - `integration-flake-repeat-<backend>`
  - Includes:
    - `integration-flake-<backend>-summary.md`
    - `integration-flake-<backend>-attempts.tsv`
    - per-attempt `go test` logs

## Current repeated probes (default)

- `scenario_multi_worker_contention`
- `scenario_duplicate_delivery_idempotency`
- `scenario_shutdown_during_delay_retry`
- `scenario_ordering_contract/scenario_ordering_single_worker_fifo`
- `scenario_ordering_contract/scenario_ordering_multi_worker_best_effort`

Current backend subset (scheduled):

- `redis`
- `rabbitmq`
- `sqs`

This subset is intentional: it provides broad timing/transport diversity while keeping scheduled runtime and CI cost reasonable.

## RC review procedure (required)

For a release candidate:

1. Review the most recent `integration-flake-repeat` workflow run.
2. Open each backend artifact summary (`integration-flake-<backend>-summary.md`).
3. Check per-scenario fail counts / flake rates.
4. If any failures occurred:
   - inspect corresponding per-attempt logs
   - classify root cause (`test assumption`, `timing budget`, `driver/backend behavior`, `real regression`)
   - file/fix before GA, or explicitly document/waive with rationale
5. Record the review result below.

## Review Log Template

Copy a block per reviewed workflow run.

```md
### YYYY-MM-DD (workflow run link)

- Scope: `integration-flake-repeat`
- Repeat count: `N`
- Backends reviewed: `redis`, `rabbitmq`, `sqs`
- Result: `pass` | `flakes observed` | `blocked`
- Notes:
  - `<backend>/<scenario>`: `<summary>`
  - Follow-up issue(s): `<link or none>`
```
