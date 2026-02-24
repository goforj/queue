#!/usr/bin/env bash
set -euo pipefail

artifacts_dir="${BENCH_SMOKE_ARTIFACTS_DIR:-.artifacts}"
mkdir -p "${artifacts_dir}"

bench_log="${artifacts_dir}/bench-smoke.log"
summary_md="${artifacts_dir}/bench-smoke-summary.md"

# Thresholds are intentionally loose regression alarms, not optimization goals.
max_enqueue_no_observer_ns="${BENCH_SMOKE_MAX_NS_OP_ENQUEUE_SYNC_NO_OBSERVER:-500000}"   # 0.5ms/op
max_enqueue_with_observer_ns="${BENCH_SMOKE_MAX_NS_OP_ENQUEUE_SYNC_WITH_OBSERVER:-1000000}" # 1.0ms/op
max_worker_lifecycle_ns="${BENCH_SMOKE_MAX_NS_OP_WORKERPOOL_LIFECYCLE:-50000000}"        # 50ms/op

go test ./ \
  -run '^$' \
  -bench 'Benchmark(EnqueueSync_NoObserver|EnqueueSync_WithObserver|WorkerpoolLifecycle)$' \
  -benchmem \
  -benchtime=200ms \
  -count=1 \
  >"${bench_log}" 2>&1

extract_ns_per_op() {
  local bench_name="$1"
  awk -v bench="${bench_name}" '
    $1 ~ ("^" bench "-[0-9]+$") {
      for (i = 1; i <= NF; i++) {
        if ($(i+1) == "ns/op") {
          print $i
          exit
        }
      }
    }
  ' "${bench_log}"
}

no_obs_ns="$(extract_ns_per_op BenchmarkEnqueueSync_NoObserver)"
with_obs_ns="$(extract_ns_per_op BenchmarkEnqueueSync_WithObserver)"
lifecycle_ns="$(extract_ns_per_op BenchmarkWorkerpoolLifecycle)"

for v in no_obs_ns with_obs_ns lifecycle_ns; do
  if [[ -z "${!v}" ]]; then
    echo "missing benchmark metric: ${v}" >&2
    echo "--- bench output ---" >&2
    cat "${bench_log}" >&2
    exit 1
  fi
done

check_threshold() {
  local name="$1"
  local value="$2"
  local limit="$3"
  if awk -v v="${value}" -v l="${limit}" 'BEGIN { exit !(v <= l) }'; then
    return 0
  fi
  echo "${name} exceeded threshold: ${value}ns/op > ${limit}ns/op" >&2
  return 1
}

status="pass"
if ! check_threshold "BenchmarkEnqueueSync_NoObserver" "${no_obs_ns}" "${max_enqueue_no_observer_ns}"; then status="fail"; fi
if ! check_threshold "BenchmarkEnqueueSync_WithObserver" "${with_obs_ns}" "${max_enqueue_with_observer_ns}"; then status="fail"; fi
if ! check_threshold "BenchmarkWorkerpoolLifecycle" "${lifecycle_ns}" "${max_worker_lifecycle_ns}"; then status="fail"; fi

{
  echo "## Benchmark Smoke Summary"
  echo
  echo "Thresholds are regression alarms, not optimization targets."
  echo
  echo "| Benchmark | ns/op | Threshold (ns/op) | Status |"
  echo "|---|---:|---:|---|"
  row_status="pass"
  if ! awk -v v="${no_obs_ns}" -v l="${max_enqueue_no_observer_ns}" 'BEGIN { exit !(v <= l) }'; then row_status="fail"; fi
  printf '| `%s` | `%s` | `%s` | %s |\n' "BenchmarkEnqueueSync_NoObserver" "${no_obs_ns}" "${max_enqueue_no_observer_ns}" "${row_status}"
  row_status="pass"
  if ! awk -v v="${with_obs_ns}" -v l="${max_enqueue_with_observer_ns}" 'BEGIN { exit !(v <= l) }'; then row_status="fail"; fi
  printf '| `%s` | `%s` | `%s` | %s |\n' "BenchmarkEnqueueSync_WithObserver" "${with_obs_ns}" "${max_enqueue_with_observer_ns}" "${row_status}"
  row_status="pass"
  if ! awk -v v="${lifecycle_ns}" -v l="${max_worker_lifecycle_ns}" 'BEGIN { exit !(v <= l) }'; then row_status="fail"; fi
  printf '| `%s` | `%s` | `%s` | %s |\n' "BenchmarkWorkerpoolLifecycle" "${lifecycle_ns}" "${max_worker_lifecycle_ns}" "${row_status}"
  echo
  echo "Source log: \`$(basename "${bench_log}")\`"
} >"${summary_md}"

echo "Wrote ${bench_log}"
echo "Wrote ${summary_md}"

if [[ "${status}" != "pass" ]]; then
  exit 1
fi
