#!/usr/bin/env bash
set -euo pipefail

# Guards the repository-wide module contract that ordinary Go commands cannot
# see because they stop at nested module boundaries.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
RELEASE_VERSION=""
TAG_VERSION=""
EXCLUDES=()

usage() {
  cat <<'USAGE'
Usage:
  scripts/check-module-inventory.sh [--release-version <vX.Y.Z>] [--exclude <module-dir>]... [--tag-version <vX.Y.Z>]

Checks:
  - every go.mod has the expected repository module path and Go version
  - go.work contains every discovered module exactly once
  - the CI race matrix contains root and every discovered driver module exactly once
  - sibling requirements use one version and resolve through local replacements
  - the release tag planner discovers every module and computes the documented tag
  - --release-version rejects sibling dependency pins that would not resolve after release
  - --exclude omits matching module owners but rejects an incomplete dependency tag set
  - --tag-version verifies an existing synchronized tag family at one commit

Examples:
  scripts/check-module-inventory.sh
  scripts/check-module-inventory.sh --release-version v0.3.0
  scripts/check-module-inventory.sh --release-version v0.3.0 --exclude examples
  scripts/check-module-inventory.sh --tag-version v0.2.1
USAGE
}

fail() {
  echo "module inventory guard: $*" >&2
  exit 1
}

require_version() {
  local version="$1"
  if ! validate_release_version "$version"; then
    fail "$RELEASE_VERSION_ERROR"
  fi
}

normalize_dir() {
  local dir="$1"
  dir="${dir#./}"
  dir="${dir%/}"
  if [[ -z "$dir" ]]; then
    dir="."
  fi
  printf '%s\n' "$dir"
}

module_is_excluded() {
  local dir="$1"
  local excluded
  for excluded in "${EXCLUDES[@]}"; do
    if [[ "$dir" == "$excluded" ]] || [[ "$dir" == "$excluded/"* ]]; then
      return 0
    fi
  done
  return 1
}

json_value() {
  local key="$1"
  awk -v key="$key" '
    $0 ~ "\"" key "\"[[:space:]]*:" {
      line = $0
      sub("^.*\"" key "\"[[:space:]]*:[[:space:]]*\"", "", line)
      sub("\".*$", "", line)
      print line
      exit
    }
  '
}

module_path_from_json() {
  awk '
    /"Module"[[:space:]]*:/ { in_module = 1; next }
    in_module && /"Path"[[:space:]]*:/ {
      line = $0
      sub("^.*\"Path\"[[:space:]]*:[[:space:]]*\"", "", line)
      sub("\".*$", "", line)
      print line
      exit
    }
  '
}

parse_edges() {
  local owner="$1"
  awk -v owner="$owner" '
    function value(line, key) {
      sub("^.*\"" key "\"[[:space:]]*:[[:space:]]*\"", "", line)
      sub("\".*$", "", line)
      return line
    }

    /^\t"Require"[[:space:]]*:/ { section = "require"; next }
    /^\t"Replace"[[:space:]]*:/ { section = "replace"; next }
    /^\t"(Exclude|Retract|Tool|Ignore)"[[:space:]]*:/ { section = ""; next }

    section == "require" && /^\t\t\{/ {
      req_path = ""
      req_version = ""
      next
    }
    section == "require" && /"Path"[[:space:]]*:/ {
      req_path = value($0, "Path")
      next
    }
    section == "require" && /"Version"[[:space:]]*:/ {
      req_version = value($0, "Version")
      next
    }
    section == "require" && /^\t\t\},?$/ {
      if (req_path != "") {
        print "require|" owner "|" req_path "|" req_version
      }
      next
    }

    section == "replace" && /"Old"[[:space:]]*:/ {
      replace_part = "old"
      old_path = ""
      old_version = ""
      new_path = ""
      new_version = ""
      next
    }
    section == "replace" && /"New"[[:space:]]*:/ {
      replace_part = "new"
      next
    }
    section == "replace" && /"Path"[[:space:]]*:/ {
      if (replace_part == "old") {
        old_path = value($0, "Path")
      } else if (replace_part == "new") {
        new_path = value($0, "Path")
      }
      next
    }
    section == "replace" && /"Version"[[:space:]]*:/ {
      if (replace_part == "old") {
        old_version = value($0, "Version")
      } else if (replace_part == "new") {
        new_version = value($0, "Version")
      }
      next
    }
    section == "replace" && /^\t\t\},?$/ {
      if (old_path != "") {
        print "replace|" owner "|" old_path "|" old_version "|" new_path "|" new_version
      }
      replace_part = ""
      next
    }
  '
}

VERSION_VALIDATOR="$ROOT_DIR/scripts/release-version.sh"
[[ -r "$VERSION_VALIDATOR" ]] || fail "scripts/release-version.sh is missing or unreadable"
# shellcheck source=scripts/release-version.sh
source "$VERSION_VALIDATOR"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    --release-version)
      RELEASE_VERSION="${2:-}"
      [[ -n "$RELEASE_VERSION" ]] || fail "--release-version requires a value"
      require_version "$RELEASE_VERSION"
      shift 2
      ;;
    --tag-version)
      TAG_VERSION="${2:-}"
      [[ -n "$TAG_VERSION" ]] || fail "--tag-version requires a value"
      require_version "$TAG_VERSION"
      shift 2
      ;;
    --exclude)
      excluded="${2:-}"
      [[ -n "$excluded" ]] || fail "--exclude requires a module directory value"
      EXCLUDES+=("$(normalize_dir "$excluded")")
      shift 2
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

if [[ ${#EXCLUDES[@]} -gt 0 && -z "$RELEASE_VERSION" ]]; then
  fail "--exclude requires --release-version"
fi

[[ -f "$ROOT_DIR/go.mod" ]] || fail "root go.mod is missing"
[[ -f "$ROOT_DIR/go.work" ]] || fail "go.work is missing"
[[ -x "$ROOT_DIR/scripts/tag-all-modules.sh" ]] || fail "scripts/tag-all-modules.sh is missing or not executable"
[[ -x "$ROOT_DIR/scripts/plan-module-release-tags.sh" ]] || fail "scripts/plan-module-release-tags.sh is missing or not executable"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

MODULES_FILE="$TMP_DIR/modules.tsv"
EDGES_FILE="$TMP_DIR/edges.tsv"
REQUIRES_FILE="$TMP_DIR/requires.tsv"
REPLACEMENTS_FILE="$TMP_DIR/replacements.tsv"
EXPECTED_DIRS_FILE="$TMP_DIR/expected-dirs.txt"
WORKSPACE_DIRS_FILE="$TMP_DIR/workspace-dirs.txt"
WORKSPACE_DIRS_RAW_FILE="$TMP_DIR/workspace-dirs-raw.txt"
EXPECTED_RACE_DIRS_FILE="$TMP_DIR/expected-race-dirs.txt"
RACE_DIRS_FILE="$TMP_DIR/race-dirs.txt"
RACE_DIRS_RAW_FILE="$TMP_DIR/race-dirs-raw.txt"

while IFS= read -r mod_file; do
  rel_file="${mod_file#./}"
  dir="$(normalize_dir "$(dirname "$rel_file")")"
  json_file="$TMP_DIR/module-${dir//\//_}.json"
  if [[ "$dir" == "." ]]; then
    json_file="$TMP_DIR/module-root.json"
  fi

  (cd "$ROOT_DIR" && GOWORK=off go mod edit -json "$rel_file") >"$json_file"
  module_path="$(module_path_from_json <"$json_file")"
  go_version="$(json_value Go <"$json_file")"
  [[ -n "$module_path" ]] || fail "could not read module path from $rel_file"
  [[ -n "$go_version" ]] || fail "could not read Go version from $rel_file"

  printf '%s\t%s\t%s\n' "$dir" "$module_path" "$go_version" >>"$MODULES_FILE"
  parse_edges "$dir" <"$json_file" >>"$EDGES_FILE"
done < <(
  cd "$ROOT_DIR"
  find . -name go.mod -type f \
    -not -path './.git/*' \
    -not -path './*/.git/*' \
    -not -path './*/vendor/*' \
    | sort
)

[[ -s "$MODULES_FILE" ]] || fail "no Go modules discovered"
awk -F '|' '$1 == "require"' "$EDGES_FILE" >"$REQUIRES_FILE"
awk -F '|' '$1 == "replace"' "$EDGES_FILE" >"$REPLACEMENTS_FILE"
root_module="$(awk -F '\t' '$1 == "." { print $2; exit }' "$MODULES_FILE")"
root_go_version="$(awk -F '\t' '$1 == "." { print $3; exit }' "$MODULES_FILE")"
[[ -n "$root_module" ]] || fail "root module was not discovered"

module_count=0
while IFS=$'\t' read -r dir module_path go_version; do
  module_count=$((module_count + 1))
  expected_path="$root_module"
  if [[ "$dir" != "." ]]; then
    expected_path="$root_module/$dir"
  fi
  [[ "$module_path" == "$expected_path" ]] || fail "$dir declares $module_path; expected $expected_path"
  [[ "$go_version" == "$root_go_version" ]] || fail "$dir uses Go $go_version; root uses Go $root_go_version"
  module_release_version="${RELEASE_VERSION:-$TAG_VERSION}"
  if [[ -n "$module_release_version" ]] && ! module_is_excluded "$dir"; then
    if ! validate_release_module_path "$module_path" "$module_release_version"; then
      fail "$RELEASE_VERSION_ERROR"
    fi
  fi
  printf '%s\n' "$dir" >>"$EXPECTED_DIRS_FILE"
done <"$MODULES_FILE"
sort -u -o "$EXPECTED_DIRS_FILE" "$EXPECTED_DIRS_FILE"

workspace_json="$TMP_DIR/workspace.json"
(cd "$ROOT_DIR" && go work edit -json "$ROOT_DIR/go.work") >"$workspace_json"
workspace_go_version="$(json_value Go <"$workspace_json")"
[[ "$workspace_go_version" == "$root_go_version" ]] || fail "go.work uses Go $workspace_go_version; root uses Go $root_go_version"

while IFS= read -r disk_path; do
  [[ -n "$disk_path" ]] || continue
  if [[ "$disk_path" == /* ]]; then
    absolute_path="$disk_path"
  else
    absolute_path="$ROOT_DIR/$disk_path"
  fi
  [[ -d "$absolute_path" ]] || fail "go.work references missing directory: $disk_path"
  canonical_path="$(cd "$absolute_path" && pwd -P)"
  if [[ "$canonical_path" == "$ROOT_DIR" ]]; then
    workspace_dir="."
  elif [[ "$canonical_path" == "$ROOT_DIR/"* ]]; then
    workspace_dir="${canonical_path#"$ROOT_DIR/"}"
  else
    fail "go.work references a module outside the repository: $disk_path"
  fi
  printf '%s\n' "$workspace_dir" >>"$WORKSPACE_DIRS_RAW_FILE"
done < <(awk '/"DiskPath"[[:space:]]*:/ { line = $0; sub("^.*\"DiskPath\"[[:space:]]*:[[:space:]]*\"", "", line); sub("\".*$", "", line); print line }' "$workspace_json")
sort -u "$WORKSPACE_DIRS_RAW_FILE" >"$WORKSPACE_DIRS_FILE"
workspace_entry_count="$(wc -l <"$WORKSPACE_DIRS_RAW_FILE" | tr -d ' ')"
workspace_unique_count="$(wc -l <"$WORKSPACE_DIRS_FILE" | tr -d ' ')"
[[ "$workspace_entry_count" -eq "$workspace_unique_count" ]] || fail "go.work contains duplicate module entries"

if ! diff -u "$EXPECTED_DIRS_FILE" "$WORKSPACE_DIRS_FILE" >"$TMP_DIR/workspace.diff"; then
  cat "$TMP_DIR/workspace.diff" >&2
  fail "go.work membership differs from the discovered module inventory"
fi

RACE_WORKFLOW="$ROOT_DIR/.github/workflows/test.yml"
[[ -f "$RACE_WORKFLOW" ]] || fail ".github/workflows/test.yml is missing"
{
  printf '.\n'
  awk -F '\t' 'index($1, "driver/") == 1 { print $1 }' "$MODULES_FILE"
} | sort -u >"$EXPECTED_RACE_DIRS_FILE"

if ! awk '
  /race_module:[[:space:]]*\[/ {
    line = $0
    sub(/^.*race_module:[[:space:]]*\[/, "", line)
    sub(/\].*$/, "", line)
    count = split(line, entries, ",")
    for (i = 1; i <= count; i++) {
      value = entries[i]
      gsub(/^[[:space:]"]+|[[:space:]"]+$/, "", value)
      if (value != "") {
        print value
      }
    }
    found = 1
    exit
  }
  END { if (!found) exit 1 }
' "$RACE_WORKFLOW" >"$RACE_DIRS_RAW_FILE"; then
  fail ".github/workflows/test.yml has no inline race_module matrix"
fi

sort -u "$RACE_DIRS_RAW_FILE" >"$RACE_DIRS_FILE"
race_entry_count="$(wc -l <"$RACE_DIRS_RAW_FILE" | tr -d ' ')"
race_unique_count="$(wc -l <"$RACE_DIRS_FILE" | tr -d ' ')"
[[ "$race_entry_count" -eq "$race_unique_count" ]] || fail "CI race matrix contains duplicate module entries"

if ! diff -u "$EXPECTED_RACE_DIRS_FILE" "$RACE_DIRS_FILE" >"$TMP_DIR/race-matrix.diff"; then
  cat "$TMP_DIR/race-matrix.diff" >&2
  fail "CI race matrix must contain root and every discovered driver module"
fi

LOCAL_VERSIONS_FILE="$TMP_DIR/local-versions.txt"
while IFS='|' read -r kind owner required_path required_version; do
  [[ "$kind" == "require" ]] || continue
  target_dir="$(awk -F '\t' -v path="$required_path" '$2 == path { print $1; exit }' "$MODULES_FILE")"
  [[ -n "$target_dir" ]] || continue

  printf '%s\n' "$required_version" >>"$LOCAL_VERSIONS_FILE"
  replacement="$(awk -F '|' -v owner="$owner" -v path="$required_path" '$2 == owner && $3 == path && $4 == "" { print; exit }' "$REPLACEMENTS_FILE")"
  [[ -n "$replacement" ]] || fail "$owner requires sibling $required_path without an unversioned local replacement"

  IFS='|' read -r _ _ _ _ new_path new_version <<<"$replacement"
  [[ -z "$new_version" ]] || fail "$owner replaces sibling $required_path with version $new_version instead of a local directory"
  [[ -n "$new_path" ]] || fail "$owner has an empty replacement target for sibling $required_path"

  owner_path="$ROOT_DIR"
  if [[ "$owner" != "." ]]; then
    owner_path="$ROOT_DIR/$owner"
  fi
  if [[ "$new_path" == /* ]]; then
    replacement_path="$new_path"
  else
    replacement_path="$owner_path/$new_path"
  fi
  [[ -d "$replacement_path" ]] || fail "$owner replacement for $required_path points to missing directory $new_path"
  replacement_path="$(cd "$replacement_path" && pwd -P)"
  expected_target_path="$ROOT_DIR"
  if [[ "$target_dir" != "." ]]; then
    expected_target_path="$ROOT_DIR/$target_dir"
  fi
  expected_target_path="$(cd "$expected_target_path" && pwd -P)"
  [[ "$replacement_path" == "$expected_target_path" ]] || fail "$owner replacement for $required_path points to $new_path, not $target_dir"

  if [[ -n "$RELEASE_VERSION" ]] && ! module_is_excluded "$owner"; then
    if [[ "$required_version" != "$RELEASE_VERSION" ]]; then
      fail "$owner requires sibling $required_path at $required_version; release $RELEASE_VERSION requires a resolvable $RELEASE_VERSION pin"
    fi
    if module_is_excluded "$target_dir"; then
      required_tag="$RELEASE_VERSION"
      if [[ "$target_dir" != "." ]]; then
        required_tag="$target_dir/$RELEASE_VERSION"
      fi
      fail "$owner is included but requires excluded sibling $required_path; release $RELEASE_VERSION would omit required tag $required_tag"
    fi
  fi
done <"$REQUIRES_FILE"

if [[ -s "$LOCAL_VERSIONS_FILE" ]]; then
  sort -u -o "$LOCAL_VERSIONS_FILE" "$LOCAL_VERSIONS_FILE"
  local_version_count="$(wc -l <"$LOCAL_VERSIONS_FILE" | tr -d ' ')"
  if [[ "$local_version_count" -ne 1 ]]; then
    echo "sibling requirement versions:" >&2
    sed 's/^/  - /' "$LOCAL_VERSIONS_FILE" >&2
    fail "sibling module requirement versions have drifted"
  fi
fi

while IFS='|' read -r kind owner old_path old_version new_path new_version; do
  [[ "$kind" == "replace" ]] || continue
  target_dir="$(awk -F '\t' -v path="$old_path" '$2 == path { print $1; exit }' "$MODULES_FILE")"
  [[ -n "$target_dir" ]] || continue
  [[ -z "$old_version" ]] || fail "$owner uses a version-specific replacement for sibling $old_path"
  [[ -z "$new_version" ]] || fail "$owner replaces sibling $old_path with non-local version $new_version"
  [[ -n "$new_path" ]] || fail "$owner has an empty replacement target for sibling $old_path"

  owner_path="$ROOT_DIR"
  if [[ "$owner" != "." ]]; then
    owner_path="$ROOT_DIR/$owner"
  fi
  if [[ "$new_path" == /* ]]; then
    replacement_path="$new_path"
  else
    replacement_path="$owner_path/$new_path"
  fi
  [[ -d "$replacement_path" ]] || fail "$owner replacement for $old_path points to missing directory $new_path"
  replacement_path="$(cd "$replacement_path" && pwd -P)"
  expected_target_path="$ROOT_DIR"
  if [[ "$target_dir" != "." ]]; then
    expected_target_path="$ROOT_DIR/$target_dir"
  fi
  expected_target_path="$(cd "$expected_target_path" && pwd -P)"
  [[ "$replacement_path" == "$expected_target_path" ]] || fail "$owner replacement for $old_path points to $new_path, not $target_dir"
done <"$REPLACEMENTS_FILE"

guard_version="${RELEASE_VERSION:-$TAG_VERSION}"
if [[ -z "$guard_version" ]]; then
  guard_major="0"
  if [[ "$root_module" == gopkg.in/* && "$root_module" =~ \.v(0|[1-9][0-9]*)$ ]]; then
    guard_major="${BASH_REMATCH[1]}"
  elif [[ "$root_module" =~ /v([2-9][0-9]*)$ ]]; then
    guard_major="${BASH_REMATCH[1]}"
  fi
  guard_version="v$guard_major.0.0-module-inventory-guard"
fi
release_output="$TMP_DIR/release-output.txt"
if ! "$ROOT_DIR/scripts/plan-module-release-tags.sh" "$guard_version" >"$release_output" 2>&1; then
  cat "$release_output" >&2
  fail "release tag planner failed"
fi

EXPECTED_TAGS_FILE="$TMP_DIR/expected-tags.txt"
ACTUAL_TAGS_FILE="$TMP_DIR/actual-tags.txt"
while IFS=$'\t' read -r dir _; do
  if [[ "$dir" == "." ]]; then
    printf '%s\n' "$guard_version" >>"$EXPECTED_TAGS_FILE"
  else
    printf '%s/%s\n' "$dir" "$guard_version" >>"$EXPECTED_TAGS_FILE"
  fi
done <"$MODULES_FILE"
sort -u "$release_output" >"$ACTUAL_TAGS_FILE"
sort -u -o "$EXPECTED_TAGS_FILE" "$EXPECTED_TAGS_FILE"
if ! diff -u "$EXPECTED_TAGS_FILE" "$ACTUAL_TAGS_FILE" >"$TMP_DIR/tags.diff"; then
  cat "$TMP_DIR/tags.diff" >&2
  fail "release tag planner does not cover the discovered module inventory"
fi

if [[ -n "$TAG_VERSION" ]]; then
  root_tag_commit="$(git -C "$ROOT_DIR" rev-parse -q --verify "refs/tags/$TAG_VERSION^{commit}")" || fail "missing root tag $TAG_VERSION"
  while IFS=$'\t' read -r dir _; do
    tag="$TAG_VERSION"
    if [[ "$dir" != "." ]]; then
      tag="$dir/$TAG_VERSION"
    fi
    tag_commit="$(git -C "$ROOT_DIR" rev-parse -q --verify "refs/tags/$tag^{commit}")" || fail "missing module tag $tag"
    [[ "$tag_commit" == "$root_tag_commit" ]] || fail "$tag points to $tag_commit; root $TAG_VERSION points to $root_tag_commit"
  done <"$MODULES_FILE"
fi

echo "module inventory guard: $module_count modules, Go $root_go_version, workspace/race/replacements/release coverage OK"
if [[ -s "$LOCAL_VERSIONS_FILE" ]]; then
  echo "module inventory guard: sibling requirement version $(head -n 1 "$LOCAL_VERSIONS_FILE")"
fi
if [[ -n "$TAG_VERSION" ]]; then
  echo "module inventory guard: tag family $TAG_VERSION resolves to $root_tag_commit"
fi
