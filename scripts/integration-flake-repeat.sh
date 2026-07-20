#!/usr/bin/env bash
set -euo pipefail

repeat_count="${FLAKE_REPEAT_COUNT:-5}"
artifacts_dir="${FLAKE_ARTIFACTS_DIR:-.artifacts}"
backend_label="${INTEGRATION_BACKEND:-}"

case "${backend_label}" in
  redis | mysql | postgres | sqlite | nats | sqs | rabbitmq) ;;
  "" | all | *,*)
    echo "INTEGRATION_BACKEND must name exactly one backend" >&2
    exit 2
    ;;
  *)
    echo "unsupported INTEGRATION_BACKEND: ${backend_label}" >&2
    exit 2
    ;;
esac

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required to validate go test JSON events" >&2
  exit 2
fi

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
  echo "Skipped attempts are documented capability gates and are excluded from the flake rate. A missing expected test event is a failure."
  echo
  echo "| Scenario | Pass | Fail | Skip | Missing | Flake Rate |"
  echo "|---|---:|---:|---:|---:|---:|"
} >"${summary_md}"

total_pass=0
total_fail=0
total_skip=0
total_missing=0
overall_exit=0

for scenario in "${scenarios[@]}"; do
  scenario_pass=0
  scenario_fail=0
  scenario_skip=0
  scenario_missing=0

  for attempt in $(seq 1 "${repeat_count}"); do
    safe_name="$(tr '/:' '__' <<<"${scenario}")"
    log_file="${artifacts_dir}/integration-flake-${backend_label}-${safe_name}-run${attempt}.jsonl"
    run_pattern="^TestIntegrationScenarios_AllBackends$/^${backend_label}$/${scenario}$"
    expected_test="TestIntegrationScenarios_AllBackends/${backend_label}/${scenario}"

    test_exit=0
    go test -tags=integration ./integration/... -run "${run_pattern}" -count=1 -json >"${log_file}" 2>&1 || test_exit=$?

    status="fail"
    if [[ "${test_exit}" -ne 0 ]]; then
      scenario_fail=$((scenario_fail + 1))
      total_fail=$((total_fail + 1))
      overall_exit=1
    elif jq -e --arg test "${expected_test}" 'select(.Action == "pass" and .Test == $test)' "${log_file}" >/dev/null; then
      status="pass"
      scenario_pass=$((scenario_pass + 1))
      total_pass=$((total_pass + 1))
    elif jq -e --arg test "${expected_test}" 'select(.Action == "skip" and .Test == $test)' "${log_file}" >/dev/null; then
      status="skip"
      scenario_skip=$((scenario_skip + 1))
      total_skip=$((total_skip + 1))
    else
      status="missing"
      scenario_missing=$((scenario_missing + 1))
      total_missing=$((total_missing + 1))
      overall_exit=1
    fi

    duration="$(jq -r 'select(.Action == "output") | .Output // empty' "${log_file}" \
      | grep -Eo '\[[^]]+\]\[[^]]+\] duration=[^[:space:]]+' \
      | tail -n 1 \
      | sed -E 's/^.* duration=([^[:space:]]+)$/\1/' || true)"
    if [[ -z "${duration}" ]]; then
      duration="n/a"
    fi

    printf "%s\t%s\t%s\t%s\t%s\t%s\n" \
      "${backend_label}" "${scenario}" "${attempt}" "${status}" "${duration}" "$(basename "${log_file}")" \
      >>"${attempts_tsv}"
  done

  flake_rate="$(awk -v p="${scenario_pass}" -v f="${scenario_fail}" 'BEGIN { t=p+f; if (t==0) { print "n/a" } else { printf "%.1f%%", (f*100)/t } }')"
  printf "| \`%s\` | %d | %d | %d | %d | \`%s\` |\n" \
    "${scenario}" "${scenario_pass}" "${scenario_fail}" "${scenario_skip}" "${scenario_missing}" "${flake_rate}" \
    >>"${summary_md}"
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
    printf "| \`%s\` | \`%s\` | %s | %s | \`%s\` | \`%s\` |\n" "${b}" "${s}" "${a}" "${st}" "${d}" "${out}"
  done
  echo
  echo "### Totals"
  echo
  echo "- Total attempts: $((total_pass + total_fail + total_skip + total_missing))"
  echo "- Pass: ${total_pass}"
  echo "- Fail: ${total_fail}"
  echo "- Skip: ${total_skip}"
  echo "- Missing expected event: ${total_missing}"
} >>"${summary_md}"

echo "Wrote ${summary_md}"
echo "Wrote ${attempts_tsv}"

# Print the computed summary to stdout so CI logs are informative on failure.
echo
echo "=== Flake Repeat Summary (${backend_label}) ==="
cat "${summary_md}"

# Print failing attempts (if any) and a tail of each failing log file.
if [[ "${overall_exit}" -ne 0 ]]; then
  echo
  echo "=== Failing Attempts (${backend_label}) ==="
  awk -F '\t' 'NR==1 || $4=="fail" || $4=="missing"' "${attempts_tsv}"

  echo
  echo "=== Failing Log Tails (${backend_label}) ==="
  awk -F '\t' 'NR>1 && ($4=="fail" || $4=="missing") {print $6}' "${attempts_tsv}" | while IFS= read -r out; do
    [[ -z "${out}" ]] && continue
    file="${artifacts_dir}/${out}"
    echo "--- ${file} ---"
    if [[ -f "${file}" ]]; then
      if ! jq -r 'select(.Action == "output") | .Output // empty' "${file}" | tail -n 120; then
        tail -n 120 "${file}"
      fi
    else
      echo "missing log file: ${file}"
    fi
  done
fi

# Keep a non-zero exit on failures so scheduled runs surface flakes in job status.
exit "${overall_exit}"
