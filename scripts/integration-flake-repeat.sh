#!/usr/bin/env bash
set -euo pipefail

repeat_count="${FLAKE_REPEAT_COUNT:-5}"
artifacts_dir="${FLAKE_ARTIFACTS_DIR:-.artifacts}"
backend_label="${INTEGRATION_BACKEND:-all}"

default_scenarios=(
  "scenario_multi_worker_contention"
  "scenario_duplicate_delivery_idempotency"
  "scenario_shutdown_during_delay_retry"
  "scenario_ordering_contract/scenario_ordering_single_worker_fifo"
  "scenario_ordering_contract/scenario_ordering_multi_worker_best_effort"
)

declare -a scenarios
if [[ -n "${FLAKE_SCENARIOS:-}" ]]; then
  IFS=',' read -r -a scenarios <<<"${FLAKE_SCENARIOS}"
else
  scenarios=("${default_scenarios[@]}")
fi

if [[ "${#scenarios[@]}" -eq 0 ]]; then
  echo "no scenarios configured" >&2
  exit 2
fi

if ! [[ "${repeat_count}" =~ ^[0-9]+$ ]] || [[ "${repeat_count}" -lt 1 ]]; then
  echo "FLAKE_REPEAT_COUNT must be a positive integer, got: ${repeat_count}" >&2
  exit 2
fi

mkdir -p "${artifacts_dir}"
summary_md="${artifacts_dir}/integration-flake-${backend_label}-summary.md"
attempts_tsv="${artifacts_dir}/integration-flake-${backend_label}-attempts.tsv"

{
  echo -e "backend\tscenario\tattempt\tstatus\tduration\ttest_output"
} >"${attempts_tsv}"

{
  echo "## Integration Flake Repeat Summary"
  echo
  echo "| Field | Value |"
  echo "|---|---|"
  echo "| Backend | \`${backend_label}\` |"
  echo "| Repeat count | \`${repeat_count}\` |"
  echo "| Scenario count | \`${#scenarios[@]}\` |"
  echo
  echo "| Scenario | Pass | Fail | Flake Rate |"
  echo "|---|---:|---:|---:|"
} >"${summary_md}"

total_pass=0
total_fail=0
overall_exit=0

for scenario in "${scenarios[@]}"; do
  scenario_pass=0
  scenario_fail=0

  for attempt in $(seq 1 "${repeat_count}"); do
    safe_name="$(tr '/:' '__' <<<"${scenario}")"
    log_file="${artifacts_dir}/integration-flake-${backend_label}-${safe_name}-run${attempt}.log"
    run_pattern="^TestIntegrationScenarios_AllBackends/.*/${scenario}$"

    status="pass"
    if go test -tags=integration ./integration/... -run "${run_pattern}" -count=1 -v >"${log_file}" 2>&1; then
      scenario_pass=$((scenario_pass + 1))
      total_pass=$((total_pass + 1))
    else
      status="fail"
      scenario_fail=$((scenario_fail + 1))
      total_fail=$((total_fail + 1))
      overall_exit=1
    fi

    duration="$(grep -Eo '\[[^]]+\]\[[^]]+\] duration=[^[:space:]]+' "${log_file}" | tail -n 1 | sed -E 's/^.* duration=([^[:space:]]+)$/\1/' || true)"
    if [[ -z "${duration}" ]]; then
      duration="n/a"
    fi

    printf "%s\t%s\t%s\t%s\t%s\t%s\n" \
      "${backend_label}" "${scenario}" "${attempt}" "${status}" "${duration}" "$(basename "${log_file}")" \
      >>"${attempts_tsv}"
  done

  flake_rate="$(awk -v p="${scenario_pass}" -v f="${scenario_fail}" 'BEGIN { t=p+f; if (t==0) { print "n/a" } else { printf "%.1f%%", (f*100)/t } }')"
  printf '| `%s` | %d | %d | `%s` |\n' "${scenario}" "${scenario_pass}" "${scenario_fail}" "${flake_rate}" >>"${summary_md}"
done

{
  echo
  echo "### Attempt Log"
  echo
  echo "Source: \`$(basename "${attempts_tsv}")\`"
  echo
  echo "| Backend | Scenario | Attempt | Status | Duration | Test Output |"
  echo "|---|---|---:|---|---|---|"
  tail -n +2 "${attempts_tsv}" | while IFS=$'\t' read -r b s a st d out; do
    printf '| `%s` | `%s` | %s | %s | `%s` | `%s` |\n' "${b}" "${s}" "${a}" "${st}" "${d}" "${out}"
  done
  echo
  echo "### Totals"
  echo
  echo "- Total attempts: $((total_pass + total_fail))"
  echo "- Pass: ${total_pass}"
  echo "- Fail: ${total_fail}"
} >>"${summary_md}"

echo "Wrote ${summary_md}"
echo "Wrote ${attempts_tsv}"

# Keep a non-zero exit on failures so scheduled runs surface flakes in job status.
exit "${overall_exit}"
