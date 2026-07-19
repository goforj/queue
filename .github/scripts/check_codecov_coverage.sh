#!/usr/bin/env bash
set -euo pipefail

# Guards the artifact fan-in contract before the single Codecov upload.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ARTIFACTS_DIR="${1:-$ROOT_DIR/coverage-artifacts}"
ROOT_MODULE="$(awk '$1 == "module" { print $2; exit }' "$ROOT_DIR/go.mod")"

fail() {
  echo "coverage artifact guard: $*" >&2
  exit 1
}

integration_backends=(null sync workerpool redis mysql postgres sqlite nats sqs rabbitmq)
driver_modules=(mysqlqueue natsqueue postgresqueue rabbitmqqueue redisqueue sqlitequeue sqlqueuecore sqsqueue)

expected_profiles=("$ARTIFACTS_DIR/coverage-unit/coverage-unit.out")
for backend in "${integration_backends[@]}"; do
  expected_profiles+=("$ARTIFACTS_DIR/coverage-integration-$backend/coverage-integration-$backend.out")
done

[[ -d "$ARTIFACTS_DIR" ]] || fail "artifact directory does not exist: $ARTIFACTS_DIR"
for profile in "${expected_profiles[@]}"; do
  [[ -s "$profile" ]] || fail "expected profile is missing or empty: $profile"
done

discovered_profile_count="$(find "$ARTIFACTS_DIR" -type f -name '*.out' -print | awk 'END { print NR }')"
[[ "$discovered_profile_count" -eq "${#expected_profiles[@]}" ]] || \
  fail "found $discovered_profile_count profiles, expected ${#expected_profiles[@]}"

validate_profile() {
  local profile="$1"
  [[ "$(head -n 1 "$profile")" == "mode: atomic" ]] || fail "profile is not atomic: $profile"
  [[ "$(grep -c '^mode:' "$profile")" -eq 1 ]] || fail "profile has multiple mode headers: $profile"

  awk -v prefix="$ROOT_MODULE/" '
    NR == 1 { next }
    NF != 3 || $2 !~ /^[0-9]+$/ || $3 !~ /^[0-9]+$/ {
      print "coverage artifact guard: malformed coverage record in " FILENAME ": " $0 > "/dev/stderr"
      exit 1
    }
    index($1, prefix) != 1 {
      print "coverage artifact guard: non-repository profile path in " FILENAME ": " $1 > "/dev/stderr"
      exit 1
    }
    seen[$1]++ {
      print "coverage artifact guard: duplicate source range in " FILENAME ": " $1 > "/dev/stderr"
      exit 1
    }
  ' "$profile"
}

for profile in "${expected_profiles[@]}"; do
  validate_profile "$profile"
done

unit_profile="$ARTIFACTS_DIR/coverage-unit/coverage-unit.out"
manifest="$ARTIFACTS_DIR/coverage-unit/coverage-unit-modules.tsv"
[[ -s "$manifest" ]] || fail "unit module manifest is missing or empty: $manifest"

expected_manifest="$({
  printf '.\t%s\n' "$ROOT_MODULE"
  for driver in "${driver_modules[@]}"; do
    printf 'driver/%s\t%s/driver/%s\n' "$driver" "$ROOT_MODULE" "$driver"
  done
  printf 'examples\t%s/examples\n' "$ROOT_MODULE"
  printf 'integration\t%s/integration\n' "$ROOT_MODULE"
} | LC_ALL=C sort)"
actual_manifest="$(awk -F '\t' '!/^#/ && NF { print $1 "\t" $2 }' "$manifest" | LC_ALL=C sort)"
if [[ "$actual_manifest" != "$expected_manifest" ]]; then
  diff -u <(printf '%s\n' "$expected_manifest") <(printf '%s\n' "$actual_manifest") >&2 || true
  fail "unit module manifest does not match the buildable module inventory"
fi

require_path() {
  local profile="$1"
  local pattern="$2"
  local label="$3"
  grep -Eq "$pattern" "$profile" || fail "$label is absent from $profile"
}

require_covered_path() {
  local pattern="$1"
  local label="$2"
  shift 2
  awk -v pattern="$pattern" '
    NR > 1 && $1 ~ pattern && ($3 + 0) > 0 { found = 1; exit }
    END { exit !found }
  ' "$@" || fail "$label has no covered source range"
}

require_covered_function() {
  local profile="$1"
  local relative_file="$2"
  local function_name="$3"
  local label="$4"
  local function_profile
  function_profile="$ARTIFACTS_DIR/functions-$(basename "$profile").txt"

  if ! GOWORK="$ROOT_DIR/go.work" go tool cover -func="$profile" >"$function_profile"; then
    fail "could not summarize functions in $profile"
  fi
  awk -v path="$ROOT_MODULE/$relative_file:" -v function_name="$function_name" '
    index($1, path) == 1 && $2 == function_name {
      percent = $3
      sub(/%$/, "", percent)
      if ((percent + 0) > 0) {
        found = 1
      }
    }
    END { exit !found }
  ' "$function_profile" || fail "$label did not execute"
  rm -f "$function_profile"
}

require_path "$unit_profile" "^${ROOT_MODULE//./[.]}/[^/]+[.]go:" "root-module source"
for driver in "${driver_modules[@]}"; do
  require_path "$unit_profile" "^${ROOT_MODULE//./[.]}/driver/$driver/.*[.]go:" "driver/$driver source"
done
require_path "$unit_profile" "^${ROOT_MODULE//./[.]}/integration/.*[.]go:" "integration-module source"

require_covered_path "^${ROOT_MODULE//./[.]}/queue[.]go:" "representative root source" "${expected_profiles[@]}"
require_covered_path "^${ROOT_MODULE//./[.]}/bus/testhooks_integration[.]go:" "root integration-tagged bus fixture" "$unit_profile"
for driver in "${driver_modules[@]}"; do
  require_covered_path "^${ROOT_MODULE//./[.]}/driver/$driver/.*[.]go:" "driver/$driver source" "${expected_profiles[@]}"
done

for backend in "${integration_backends[@]}"; do
  profile="$ARTIFACTS_DIR/coverage-integration-$backend/coverage-integration-$backend.out"
  dialect_evidence_file=""
  dialect_evidence_function=""
  case "$backend" in
    null)
      evidence_file="queue_null.go"
      evidence_function="Dispatch"
      ;;
    sync)
      evidence_file="queue_local.go"
      evidence_function="enqueueNow"
      ;;
    workerpool)
      evidence_file="queue_local.go"
      evidence_function="worker"
      ;;
    redis)
      evidence_file="driver/redisqueue/worker_redis_impl.go"
      evidence_function="StartWorkers"
      ;;
    mysql)
      evidence_file="driver/sqlqueuecore/queue_database_impl.go"
      evidence_function="workerLoop"
      dialect_evidence_file="driver/mysqlqueue/mysqlqueue.go"
      dialect_evidence_function="NewWithConfig"
      ;;
    postgres)
      evidence_file="driver/sqlqueuecore/queue_database_impl.go"
      evidence_function="workerLoop"
      dialect_evidence_file="driver/postgresqueue/postgresqueue.go"
      dialect_evidence_function="NewWithConfig"
      ;;
    sqlite)
      evidence_file="driver/sqlqueuecore/queue_database_impl.go"
      evidence_function="workerLoop"
      dialect_evidence_file="driver/sqlitequeue/sqlitequeue.go"
      dialect_evidence_function="New"
      ;;
    nats)
      evidence_file="driver/natsqueue/worker_nats_impl.go"
      evidence_function="processMessage"
      ;;
    sqs)
      evidence_file="driver/sqsqueue/worker_sqs_impl.go"
      evidence_function="process"
      ;;
    rabbitmq)
      evidence_file="driver/rabbitmqqueue/worker_rabbitmq_impl.go"
      evidence_function="processDelivery"
      ;;
    *) fail "coverage evidence is not defined for backend $backend" ;;
  esac
  require_covered_function "$profile" "$evidence_file" "$evidence_function" "$backend integration backend"
  if [[ -n "$dialect_evidence_file" ]]; then
    require_covered_function "$profile" "$dialect_evidence_file" "$dialect_evidence_function" "$backend SQL dialect"
  fi
done

echo "coverage artifact guard: 1 multi-module unit profile and ${#integration_backends[@]} backend profiles verified"
