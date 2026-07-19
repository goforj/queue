#!/usr/bin/env bash
set -euo pipefail

# Codecov accepts uploads asynchronously, so a successful uploader process does
# not prove that the report for this workflow run is ready. The mutable pull
# request comment remains an eventually consistent presentation of this data.

fail() {
  echo "Codecov report guard: $*" >&2
  exit 1
}

require_sha() {
  local label="$1"
  local value="$2"
  [[ "$value" =~ ^[0-9a-f]{40}$ ]] || fail "$label must be a full lowercase commit SHA"
}

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

sha="${CODECOV_SHA:-}"
base_sha="${CODECOV_BASE_SHA:-}"
upload_name="${CODECOV_UPLOAD_NAME:-}"
repository="${GITHUB_REPOSITORY:-goforj/queue}"
timeout_seconds="${CODECOV_WAIT_TIMEOUT_SECONDS:-180}"
poll_seconds="${CODECOV_WAIT_POLL_SECONDS:-5}"

require_sha "CODECOV_SHA" "$sha"
[[ -n "$upload_name" ]] || fail "CODECOV_UPLOAD_NAME is required"
[[ "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || \
  fail "GITHUB_REPOSITORY must have the form owner/repository"
[[ "$timeout_seconds" =~ ^[1-9][0-9]*$ ]] || \
  fail "CODECOV_WAIT_TIMEOUT_SECONDS must be a positive integer"
[[ "$poll_seconds" =~ ^[1-9][0-9]*$ ]] || \
  fail "CODECOV_WAIT_POLL_SECONDS must be a positive integer"

if [[ -n "$base_sha" ]]; then
  require_sha "CODECOV_BASE_SHA" "$base_sha"
fi

owner="${repository%%/*}"
repo="${repository#*/}"
api_base="${CODECOV_API_BASE:-https://api.codecov.io/api/v2/github/$owner/repos/$repo}"
api_base="${api_base%/}"
deadline=$((SECONDS + timeout_seconds))

upload_ready=false
commit_ready=false
comparison_ready=false
[[ -z "$base_sha" ]] && comparison_ready=true

upload_state="not found"
commit_state="not found"
comparison_state="not requested"
merged_sessions=0
commit_json=""
comparison_json=""

fetch_json() {
  local url="$1"
  curl --fail --silent --show-error \
    --connect-timeout 10 \
    --max-time 20 \
    "$url" 2>/dev/null | jq -ce '.' 2>/dev/null
}

fetch_uploads() {
  local page_size=150
  local results='[]'
  local page_json
  local page_results
  local expected_count=-1
  local result_count
  local page=1
  local page_count

  while :; do
    page_json="$(fetch_json \
      "$api_base/commits/$sha/uploads/?page=$page&page_size=$page_size"
    )" || return 1
    jq -e '
      (.results | type) == "array" and
      (.count | type) == "number" and
      (.count >= 0) and
      ((.count % 1) == 0)
    ' >/dev/null <<<"$page_json" || return 1

    page_count="$(jq '.count' <<<"$page_json")" || return 1
    if (( expected_count < 0 )); then
      expected_count="$page_count"
    elif (( page_count != expected_count )); then
      return 1
    fi

    page_results="$(jq -c '.results' <<<"$page_json")" || return 1
    results="$(jq -cn \
      --argjson previous "$results" \
      --argjson current "$page_results" \
      '$previous + $current'
    )" || return 1

    result_count="$(jq 'length' <<<"$results")" || return 1
    (( result_count >= expected_count )) && break
    (( page < 20 )) || return 1
    page=$((page + 1))
  done

  result_count="$(jq 'length' <<<"$results")" || return 1
  (( result_count == expected_count )) || return 1
  jq -cn --argjson results "$results" '{results: $results}'
}

echo "Codecov report guard: waiting for upload $upload_name at $sha"

while (( SECONDS < deadline )); do
  previous_upload_ready="$upload_ready"
  upload_ready=false
  if uploads_json="$(fetch_uploads)"; then
    if jq -e '(.results | type) == "array"' >/dev/null <<<"$uploads_json"; then
      upload_state="$(jq -r --arg name "$upload_name" '
        [.results[]? | select(.name == $name) | (.state_name // .state // "unknown")] |
        last // "not found"
      ' <<<"$uploads_json")"
      if jq -e --arg name "$upload_name" '
        any(.results[]?;
          .name == $name and
          ((.state_name // "") == "MERGED" or (.state // "") == "merged") and
          ((.totals | type) == "object")
        )
      ' >/dev/null <<<"$uploads_json"; then
        merged_sessions="$(jq '
          [.results[]? |
            select(
              ((.state_name // "") == "MERGED" or (.state // "") == "merged") and
              ((.totals | type) == "object")
            )
          ] | length
        ' <<<"$uploads_json")"
        upload_ready=true
        if [[ "$previous_upload_ready" != true ]]; then
          echo "Codecov report guard: upload merged"
        fi
      fi
    fi
  else
    upload_state="API unavailable or invalid"
  fi

  commit_ready=false
  if [[ "$upload_ready" == true ]]; then
    if commit_json="$(fetch_json "$api_base/commits/$sha/")" && \
      jq -e 'type == "object" and (.totals | type) == "object"' \
        >/dev/null <<<"$commit_json"; then
      commit_state="$(jq -r --argjson expected "$merged_sessions" '
        "\(.state // "unknown"), \(.totals.sessions // 0)/\($expected) sessions"
      ' <<<"$commit_json")"
      if jq -e --arg sha "$sha" --argjson expected "$merged_sessions" '
        def complete_coverage:
          [.files, .lines, .hits, .misses, .partials, .coverage] |
          all(.[]; type == "number");
        .commitid == $sha and
        .state == "complete" and
        (.totals | complete_coverage) and
        ((.totals.sessions // 0) >= $expected) and
        ($expected >= 1)
      ' >/dev/null <<<"$commit_json"; then
        commit_ready=true
      fi
    else
      commit_state="API unavailable or invalid"
    fi
  fi

  comparison_ready=false
  [[ -z "$base_sha" ]] && comparison_ready=true
  if [[ "$commit_ready" == true && -n "$base_sha" ]]; then
    if comparison_json="$(fetch_json "$api_base/compare/?base=$base_sha&head=$sha")" && \
      jq -e 'type == "object" and (.totals | type) == "object"' \
        >/dev/null <<<"$comparison_json"; then
      comparison_state="$(jq -r '
        if (.totals.patch | type) == "object"
        then "ready"
        else "totals incomplete"
        end
      ' <<<"$comparison_json")"
      if jq -e \
        --arg base "$base_sha" \
        --arg head "$sha" \
        --argjson expected "$merged_sessions" \
        --argjson commit "$commit_json" '
        def coverage_totals:
          [.files, .lines, .hits, .misses, .partials, .coverage];
        def complete_patch:
          [.files, .lines, .hits, .misses, .partials, .coverage] |
          all(.[]; type == "number");
        .base_commit == $base and
        .head_commit == $head and
        (.totals | type) == "object" and
        ((.totals.patch | type) == "object") and
        (.totals.patch | complete_patch) and
        ((.totals.head.sessions // 0) >= $expected) and
        ((.totals.head.sessions // 0) == ($commit.totals.sessions // -1)) and
        (.totals.head | coverage_totals) == ($commit.totals | coverage_totals)
      ' >/dev/null <<<"$comparison_json"; then
        comparison_ready=true
      fi
    else
      comparison_state="API unavailable or invalid"
    fi
  fi

  if [[ "$upload_ready" == true && "$commit_ready" == true && "$comparison_ready" == true ]]; then
    echo "Codecov report guard: exact commit report complete"
    if [[ -n "$base_sha" ]]; then
      echo "Codecov report guard: exact pull request comparison ready"
    fi
    project_coverage="$(jq -r '.totals.coverage // "n/a"' <<<"$commit_json")"
    patch_coverage="n/a"
    if [[ -n "$base_sha" ]]; then
      patch_coverage="$(jq -r '.totals.patch.coverage // "n/a"' <<<"$comparison_json")"
    fi

    echo "Codecov report guard: project ${project_coverage}%, patch ${patch_coverage}%"
    if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
      {
        echo "### Codecov report"
        echo
        echo "| Commit | Project | Patch |"
        echo "| --- | ---: | ---: |"
        echo "| \`$sha\` | ${project_coverage}% | ${patch_coverage}% |"
      } >>"$GITHUB_STEP_SUMMARY"
    fi
    exit 0
  fi

  sleep "$poll_seconds"
done

fail "timed out after ${timeout_seconds}s (upload: $upload_state; commit: $commit_state; comparison: $comparison_state)"
