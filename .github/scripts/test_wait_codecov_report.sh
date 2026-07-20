#!/usr/bin/env bash
set -euo pipefail

head_sha="2222222222222222222222222222222222222222"
base_sha="1111111111111111111111111111111111111111"

# fake_curl keeps the guard tests deterministic while preserving the same
# process boundary and PATH lookup used by the real curl executable.
fake_curl() {
  local url="${!#}"

  if [[ "${CODECOV_TEST_SCENARIO:-}" == "malformed" ]]; then
    echo "temporarily not JSON"
    return
  fi

  case "$url" in
    */uploads/*)
      fake_uploads "$url"
      ;;
    */compare/*)
      fake_comparison
      ;;
    */commits/*)
      fake_commit
      ;;
    *)
      return 22
      ;;
  esac
}

# fake_uploads emits single-run, rerun, and paginated upload inventories.
fake_uploads() {
  local url="$1"

  case "$CODECOV_TEST_SCENARIO" in
    aggregate|stale)
      echo '{"count":2,"results":[{"name":"prior-run","state_name":"MERGED","totals":{"files":10,"lines":100,"hits":80,"misses":10,"partials":10,"coverage":80}},{"name":"current-run","state_name":"MERGED","totals":{"files":10,"lines":100,"hits":85,"misses":10,"partials":5,"coverage":85}}]}'
      ;;
    pagination)
      if [[ "$url" == *"page=1&"* ]]; then
        jq -cn '{count:151,results:[range(0;150) | {name:("old-" + tostring),state_name:"MERGED",totals:{files:1,lines:1,hits:1,misses:0,partials:0,coverage:100}}]}'
      else
        echo '{"count":151,"results":[{"name":"page-two-run","state_name":"MERGED","totals":{"files":1,"lines":1,"hits":1,"misses":0,"partials":0,"coverage":100}}]}'
      fi
      ;;
    *)
      echo '{"count":1,"results":[{"name":"single-run","state_name":"MERGED","totals":{"files":10,"lines":100,"hits":80,"misses":10,"partials":10,"coverage":80}}]}'
      ;;
  esac
}

# fake_commit distinguishes a current aggregate from its individual uploads.
fake_commit() {
  case "$CODECOV_TEST_SCENARIO" in
    aggregate|stale)
      echo "{\"commitid\":\"$head_sha\",\"state\":\"complete\",\"totals\":{\"files\":10,\"lines\":100,\"hits\":90,\"misses\":5,\"partials\":5,\"coverage\":90,\"sessions\":2}}"
      ;;
    pagination)
      echo "{\"commitid\":\"$head_sha\",\"state\":\"complete\",\"totals\":{\"files\":1,\"lines\":1,\"hits\":1,\"misses\":0,\"partials\":0,\"coverage\":100,\"sessions\":151}}"
      ;;
    *)
      echo "{\"commitid\":\"$head_sha\",\"state\":\"complete\",\"totals\":{\"files\":10,\"lines\":100,\"hits\":80,\"misses\":10,\"partials\":10,\"coverage\":80,\"sessions\":1}}"
      ;;
  esac
}

# fake_comparison exposes both complete and intentionally stale comparison data.
fake_comparison() {
  case "$CODECOV_TEST_SCENARIO" in
    aggregate)
      echo "{\"base_commit\":\"$base_sha\",\"head_commit\":\"$head_sha\",\"totals\":{\"head\":{\"files\":10,\"lines\":100,\"hits\":90,\"misses\":5,\"partials\":5,\"coverage\":90,\"sessions\":2},\"patch\":{\"files\":2,\"lines\":5,\"hits\":5,\"misses\":0,\"partials\":0,\"coverage\":100}}}"
      ;;
    stale)
      echo "{\"base_commit\":\"$base_sha\",\"head_commit\":\"$head_sha\",\"totals\":{\"head\":{\"files\":10,\"lines\":100,\"hits\":80,\"misses\":10,\"partials\":10,\"coverage\":80,\"sessions\":1},\"patch\":{\"files\":2,\"lines\":5,\"hits\":4,\"misses\":1,\"partials\":0,\"coverage\":80}}}"
      ;;
    null-patch)
      echo "{\"base_commit\":\"$base_sha\",\"head_commit\":\"$head_sha\",\"totals\":{\"head\":{\"files\":10,\"lines\":100,\"hits\":80,\"misses\":10,\"partials\":10,\"coverage\":80,\"sessions\":1},\"patch\":null}}"
      ;;
    pagination)
      echo "{\"base_commit\":\"$base_sha\",\"head_commit\":\"$head_sha\",\"totals\":{\"head\":{\"files\":1,\"lines\":1,\"hits\":1,\"misses\":0,\"partials\":0,\"coverage\":100,\"sessions\":151},\"patch\":{\"files\":1,\"lines\":1,\"hits\":1,\"misses\":0,\"partials\":0,\"coverage\":100}}}"
      ;;
    *)
      echo "{\"base_commit\":\"$base_sha\",\"head_commit\":\"$head_sha\",\"totals\":{\"head\":{\"files\":10,\"lines\":100,\"hits\":80,\"misses\":10,\"partials\":10,\"coverage\":80,\"sessions\":1},\"patch\":{\"files\":2,\"lines\":5,\"hits\":4,\"misses\":1,\"partials\":0,\"coverage\":80}}}"
      ;;
  esac
}

if [[ "${0##*/}" == "curl" ]]; then
  fake_curl "$@"
  exit
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
guard="$script_dir/wait_codecov_report.sh"
fake_bin="$(mktemp -d "${TMPDIR:-/tmp}/queue-codecov-guard.XXXXXX")"
trap 'rm -rf -- "$fake_bin"' EXIT
ln -s "$script_dir/test_wait_codecov_report.sh" "$fake_bin/curl"

# run_guard isolates the API scenario while keeping guard timeouts short.
run_guard() {
  local scenario="$1"
  local upload_name="$2"
  local requested_base="${3-$base_sha}"

  PATH="$fake_bin:$PATH" \
    CODECOV_API_BASE="https://mock.invalid" \
    CODECOV_BASE_SHA="$requested_base" \
    CODECOV_SHA="$head_sha" \
    CODECOV_TEST_SCENARIO="$scenario" \
    CODECOV_UPLOAD_NAME="$upload_name" \
    CODECOV_WAIT_POLL_SECONDS=1 \
    CODECOV_WAIT_TIMEOUT_SECONDS=1 \
    "$guard"
}

# require_success fails with the captured guard diagnostics when acceptance regresses.
require_success() {
  local scenario="$1"
  local upload_name="$2"
  local requested_base="${3-$base_sha}"
  local expected="${4:-exact commit report complete}"
  local output

  if ! output="$(run_guard "$scenario" "$upload_name" "$requested_base" 2>&1)"; then
    echo "scenario $scenario unexpectedly failed:" >&2
    echo "$output" >&2
    exit 1
  fi
  if [[ "$output" != *"$expected"* ]]; then
    echo "scenario $scenario omitted expected output: $expected" >&2
    echo "$output" >&2
    exit 1
  fi
}

# require_failure protects against accepting stale or structurally incomplete reports.
require_failure() {
  local scenario="$1"
  local upload_name="$2"
  local expected="$3"
  local output

  if output="$(run_guard "$scenario" "$upload_name" 2>&1)"; then
    echo "scenario $scenario unexpectedly passed:" >&2
    echo "$output" >&2
    exit 1
  fi
  if [[ "$output" != *"timed out after 1s"* || "$output" != *"$expected"* ]]; then
    echo "scenario $scenario failed for the wrong reason:" >&2
    echo "$output" >&2
    exit 1
  fi
}

require_success single single-run "$base_sha" "project 80%, patch 80%"
require_success aggregate current-run "$base_sha" "project 90%, patch 100%"
require_success pagination page-two-run "$base_sha" "project 100%, patch 100%"
require_success single single-run "" "project 80%, patch n/a%"
require_failure stale current-run "comparison: ready"
require_failure null-patch single-run "comparison: totals incomplete"
require_failure malformed single-run "upload: API unavailable or invalid"

echo "Codecov report guard tests passed"
