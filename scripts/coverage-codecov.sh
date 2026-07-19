#!/usr/bin/env bash
set -euo pipefail

# Collects deterministic Codecov profiles across this repository's Go modules.
#
# Unit mode runs every buildable module without crossing go.mod boundaries and
# emits one profile plus a module manifest. Integration mode runs the real,
# tagged integration module for one backend (or "all") and emits one profile.
# Both modes collapse duplicate source ranges produced when -coverpkg spans
# multiple test binaries.
#
# Usage:
#   scripts/coverage-codecov.sh unit
#   INTEGRATION_BACKEND=redis scripts/coverage-codecov.sh integration

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="${1:-unit}"
OUTPUT_DIR="${COVERAGE_OUTPUT_DIR:-$ROOT_DIR/coverage}"
TMP_PARENT="${COVERAGE_TMP_DIR:-/tmp}"
GOCACHE_DIR="${GOCACHE:-${GOCACHE_DIR:-/tmp/gocache}}"
GOMODCACHE_DIR="${GOMODCACHE:-/tmp/gomodcache}"
ROOT_MODULE="$(awk '$1 == "module" { print $2; exit }' "$ROOT_DIR/go.mod")"

fail() {
  echo "coverage collection: $*" >&2
  exit 1
}

absolute_output_path() {
  local path="$1"
  if [[ "$path" == /* ]]; then
    printf '%s\n' "$path"
    return
  fi
  printf '%s/%s\n' "$ROOT_DIR" "$path"
}

validate_profile() {
  local profile="$1"
  [[ -s "$profile" ]] || fail "profile is missing or empty: $profile"
  [[ "$(head -n 1 "$profile")" == "mode: atomic" ]] || fail "profile is not atomic: $profile"
  [[ "$(grep -c '^mode:' "$profile")" -eq 1 ]] || fail "profile has multiple mode headers: $profile"

  awk -v prefix="$ROOT_MODULE/" '
    NR == 1 { next }
    index($1, prefix) != 1 {
      print "coverage collection: profile path is outside the repository: " $1 > "/dev/stderr"
      exit 1
    }
    seen[$1]++ {
      print "coverage collection: duplicate source range in profile: " $1 > "/dev/stderr"
      exit 1
    }
  ' "$profile"
}

merge_profiles() {
  local output="$1"
  shift
  local profiles=("$@")
  [[ "${#profiles[@]}" -gt 0 ]] || fail "no profiles were provided for $output"

  local records_file="$TMP_ROOT/records.txt"
  local sorted_file="$TMP_ROOT/records.sorted.txt"
  local merged_file="$TMP_ROOT/merged.out"
  : >"$records_file"

  local profile
  for profile in "${profiles[@]}"; do
    [[ -s "$profile" ]] || fail "raw profile is missing or empty: $profile"
    [[ "$(head -n 1 "$profile")" == "mode: atomic" ]] || fail "raw profile is not atomic: $profile"
    tail -n +2 "$profile" >>"$records_file"
  done

  LC_ALL=C sort -k1,1 "$records_file" >"$sorted_file"
  {
    printf 'mode: atomic\n'
    awk '
      function emit() {
        if (range_key != "") {
          printf "%s %s %.0f\n", range_key, statements, hits
        }
      }
      {
        if ($1 != range_key) {
          emit()
          range_key = $1
          statements = $2
          hits = $3
          next
        }
        if ($2 != statements) {
          print "coverage collection: statement count mismatch for " range_key > "/dev/stderr"
          exit 1
        }
        hits += $3
      }
      END { emit() }
    ' "$sorted_file"
  } >"$merged_file"

  mkdir -p "$(dirname "$output")"
  cp "$merged_file" "$output"
  validate_profile "$output"
}

print_summary() {
  local profile="$1"
  local ranges
  ranges="$(awk 'END { print NR - 1 }' "$profile")"
  echo "==> wrote $profile ($ranges source ranges)"
  (cd "$ROOT_DIR" && GOWORK="$ROOT_DIR/go.work" go tool cover -func="$profile" | tail -n 1)
}

collect_unit() {
  local output
  output="$(absolute_output_path "${COVERAGE_OUTPUT:-$OUTPUT_DIR/coverage-unit.out}")"
  local manifest
  manifest="$(absolute_output_path "${COVERAGE_MODULE_MANIFEST:-$OUTPUT_DIR/coverage-unit-modules.tsv}")"
  local manifest_tmp="$TMP_ROOT/unit-modules.tsv"
  printf '# module_directory\tmodule_path\n' >"$manifest_tmp"

  "$ROOT_DIR/scripts/check-module-inventory.sh"

  local raw_profiles=()
  local module_count=0
  local module_file module_dir relative_dir module_path package_list slug raw_profile list_stderr
  while IFS= read -r module_file; do
    module_count=$((module_count + 1))
    module_dir="$(dirname "$module_file")"
    relative_dir="${module_dir#"$ROOT_DIR"}"
    relative_dir="${relative_dir#/}"
    if [[ -z "$relative_dir" ]]; then
      relative_dir="."
    fi
    module_path="$(awk '$1 == "module" { print $2; exit }' "$module_file")"
    [[ -n "$module_path" ]] || fail "could not read module path from $module_file"

    list_stderr="$TMP_ROOT/list-${relative_dir//\//-}.stderr"
    if ! package_list="$({
      cd "$module_dir"
      GOWORK=off GOCACHE="$GOCACHE_DIR" GOMODCACHE="$GOMODCACHE_DIR" go list ./...
    } 2>"$list_stderr")"; then
      cat "$list_stderr" >&2
      fail "could not list packages in $relative_dir"
    fi
    if [[ -z "$package_list" ]]; then
      echo "==> $relative_dir has no buildable packages; skipping coverage"
      continue
    fi

    slug="${relative_dir//\//-}"
    if [[ "$slug" == "." ]]; then
      slug="root"
    fi
    raw_profile="$TMP_ROOT/unit-$slug.out"
    echo "==> unit coverage: $relative_dir"
    (
      cd "$module_dir"
      GOWORK=off GOCACHE="$GOCACHE_DIR" GOMODCACHE="$GOMODCACHE_DIR" \
        go test -count=1 -covermode=atomic -coverpkg=./... -coverprofile="$raw_profile" ./...
    )
    raw_profiles+=("$raw_profile")
    printf '%s\t%s\n' "$relative_dir" "$module_path" >>"$manifest_tmp"
  done < <(
    find "$ROOT_DIR" -type f -name go.mod \
      -not -path '*/.git/*' \
      -not -path '*/vendor/*' \
      -print | LC_ALL=C sort
  )
  [[ "$module_count" -gt 0 ]] || fail "no Go modules were discovered"

  merge_profiles "$output" "${raw_profiles[@]}"
  mkdir -p "$(dirname "$manifest")"
  cp "$manifest_tmp" "$manifest"
  print_summary "$output"
}

collect_integration() {
  local backend="${INTEGRATION_BACKEND:-all}"
  case "$backend" in
    all|null|sync|workerpool|redis|mysql|postgres|sqlite|nats|sqs|rabbitmq) ;;
    *) fail "unsupported INTEGRATION_BACKEND for coverage: $backend" ;;
  esac

  local output
  output="$(absolute_output_path "${COVERAGE_OUTPUT:-$OUTPUT_DIR/coverage-integration-$backend.out}")"
  local raw_profile="$TMP_ROOT/integration-$backend.raw.out"

  echo "==> integration coverage: $backend"
  (
    cd "$ROOT_DIR/integration"
    INTEGRATION_BACKEND="$backend" GOWORK=off GOCACHE="$GOCACHE_DIR" GOMODCACHE="$GOMODCACHE_DIR" \
      go test -p=1 -count=1 -tags=integration -covermode=atomic \
        -coverpkg="$ROOT_MODULE/..." -coverprofile="$raw_profile" ./...
  )

  merge_profiles "$output" "$raw_profile"
  print_summary "$output"
}

mkdir -p "$TMP_PARENT"
TMP_ROOT="$(mktemp -d "$TMP_PARENT/queue-codecov.XXXXXX")"
trap 'rm -rf -- "$TMP_ROOT"' EXIT

case "$MODE" in
  unit) collect_unit ;;
  integration) collect_integration ;;
  *) fail "usage: scripts/coverage-codecov.sh [unit|integration]" ;;
esac
