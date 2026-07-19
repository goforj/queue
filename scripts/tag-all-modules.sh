#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
VERSION_VALIDATOR="$SCRIPT_DIR/release-version.sh"
if [[ ! -r "$VERSION_VALIDATOR" ]]; then
  echo "error: release version validator is missing or unreadable: $VERSION_VALIDATOR" >&2
  exit 1
fi
# shellcheck source=scripts/release-version.sh
source "$VERSION_VALIDATOR"

usage() {
  cat <<'USAGE'
Usage:
  scripts/tag-all-modules.sh <version> [--push] [--remote <name>] [--dry-run] [--allow-dirty] [--skip-existing] [--exclude <module-dir>]

Examples:
  scripts/tag-all-modules.sh v0.1.3 --dry-run
  scripts/tag-all-modules.sh v0.1.3 --push
  scripts/tag-all-modules.sh v0.1.3 --push --exclude docs --exclude examples
  scripts/tag-all-modules.sh v0.1.3 --exclude driver

Behavior:
  - Tags root module as: vX.Y.Z
  - Tags each submodule as: <relative/module/path>/vX.Y.Z
  - Validates and tags one captured HEAD commit; dry runs preview the working tree
  - Refuses to tag modules whose sibling requirements are not pinned to the release version
  - --allow-dirty is accepted only with --dry-run because real tags always target HEAD
  - --exclude supports exact module dirs and prefixes (for example: driver excludes all driver/* modules)
USAGE
}

if [[ $# -lt 1 ]]; then
  usage
  exit 1
fi

version=""
push=0
remote="origin"
dry_run=0
allow_dirty=0
skip_existing=0
excludes=()
release_view_dir=""

cleanup() {
  if [[ -n "$release_view_dir" && -d "$release_view_dir" ]]; then
    rm -rf -- "$release_view_dir"
  fi
}
trap cleanup EXIT

normalize_module_dir() {
  local dir="$1"
  dir="${dir#./}"
  dir="${dir%/}"
  if [[ -z "$dir" ]]; then
    dir="."
  fi
  printf '%s\n' "$dir"
}

REMOTE_TAG_EXISTS=0
REMOTE_TAG_COMMIT=""

inspect_remote_tag() {
  local remote_name="$1"
  local tag="$2"
  local output
  local status
  local direct_ref="refs/tags/$tag"
  local peeled_ref="refs/tags/$tag^{}"

  REMOTE_TAG_EXISTS=0
  REMOTE_TAG_COMMIT=""
  if output="$(git ls-remote --exit-code --tags "$remote_name" "$direct_ref" "$peeled_ref")"; then
    REMOTE_TAG_EXISTS=1
  else
    status=$?
    if [[ "$status" -eq 2 ]]; then
      return 0
    fi
    echo "error: failed to query remote for tag $tag (git ls-remote exit $status)" >&2
    return "$status"
  fi

  REMOTE_TAG_COMMIT="$(awk -v ref="$peeled_ref" '$2 == ref { print $1; exit }' <<<"$output")"
  if [[ -z "$REMOTE_TAG_COMMIT" ]]; then
    REMOTE_TAG_COMMIT="$(awk -v ref="$direct_ref" '$2 == ref { print $1; exit }' <<<"$output")"
  fi
  if [[ -z "$REMOTE_TAG_COMMIT" ]]; then
    echo "error: remote returned no resolvable object for tag $tag" >&2
    return 1
  fi
}

WORKTREE_STATUS=""

capture_worktree_status() {
  local phase="$1"
  local status

  if WORKTREE_STATUS="$(git status --porcelain)"; then
    return 0
  else
    status=$?
  fi
  echo "error: failed to inspect working tree $phase (git status exit $status)" >&2
  return "$status"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    --push)
      push=1
      shift
      ;;
    --remote)
      remote="${2:-}"
      if [[ -z "$remote" ]]; then
        echo "error: --remote requires a value" >&2
        exit 1
      fi
      shift 2
      ;;
    --dry-run)
      dry_run=1
      shift
      ;;
    --allow-dirty)
      allow_dirty=1
      shift
      ;;
    --skip-existing)
      skip_existing=1
      shift
      ;;
    --exclude)
      mod="${2:-}"
      if [[ -z "$mod" ]]; then
        echo "error: --exclude requires a module directory value" >&2
        exit 1
      fi
      excludes+=("$(normalize_module_dir "$mod")")
      shift 2
      ;;
    v*)
      if [[ -n "$version" ]]; then
        echo "error: multiple versions provided" >&2
        exit 1
      fi
      version="$1"
      shift
      ;;
    *)
      echo "error: unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

if [[ -z "$version" ]]; then
  echo "error: version is required (example: v0.1.3)" >&2
  exit 1
fi

if ! validate_release_version "$version"; then
  echo "error: $RELEASE_VERSION_ERROR" >&2
  exit 1
fi

if [[ "$allow_dirty" -eq 1 && "$dry_run" -eq 0 ]]; then
  echo "error: --allow-dirty is only supported with --dry-run; real tags must be validated from HEAD" >&2
  exit 1
fi

root="$(git rev-parse --show-toplevel)"
cd "$root"

if [[ ! -f go.mod ]]; then
  echo "error: must run inside a Go module repository" >&2
  exit 1
fi

if [[ "$allow_dirty" -eq 0 ]]; then
  if capture_worktree_status "before release planning"; then
    if [[ -n "$WORKTREE_STATUS" ]]; then
      echo "error: working tree is dirty. commit/stash before tagging or pass --allow-dirty with --dry-run" >&2
      exit 1
    fi
  else
    exit $?
  fi
fi

head_commit="$(git rev-parse --verify HEAD)"

release_root="$root"
if [[ "$dry_run" -eq 0 ]]; then
  release_view_dir="$(mktemp -d "${TMPDIR:-/tmp}/queue-release-head.XXXXXX")"
  if ! git archive --format=tar "$head_commit" | tar -xf - -C "$release_view_dir"; then
    echo "error: failed to extract captured HEAD $head_commit for release validation" >&2
    exit 1
  fi
  release_root="$release_view_dir"
fi

inventory_guard="$release_root/scripts/check-module-inventory.sh"
if [[ ! -x "$inventory_guard" ]]; then
  echo "error: release preflight is missing or not executable: $inventory_guard" >&2
  exit 1
fi

tag_planner="$release_root/scripts/plan-module-release-tags.sh"
if [[ ! -x "$tag_planner" ]]; then
  echo "error: release tag planner is missing or not executable: $tag_planner" >&2
  exit 1
fi

preflight_args=(--release-version "$version")
tag_plan_args=("$version")
for excluded in "${excludes[@]}"; do
  preflight_args+=(--exclude "$excluded")
  tag_plan_args+=(--exclude "$excluded")
done
if ! preflight_output="$("$inventory_guard" "${preflight_args[@]}" 2>&1)"; then
  printf '%s\n' "$preflight_output" >&2
  exit 1
fi

if ! tag_plan="$("$tag_planner" "${tag_plan_args[@]}")"; then
  exit 1
fi

planned_tags=()
if [[ -n "$tag_plan" ]]; then
  while IFS= read -r tag; do
    planned_tags+=("$tag")
  done <<<"$tag_plan"
fi

tags_to_create=()
tags_to_push=()
for tag in "${planned_tags[@]}"; do
  if ! git check-ref-format "refs/tags/$tag" >/dev/null 2>&1; then
    echo "error: computed invalid tag ref: $tag" >&2
    exit 1
  fi

  local_exists=0
  local_tag_commit=""
  remote_exists=0
  remote_tag_commit=""

  if git rev-parse -q --verify "refs/tags/$tag" >/dev/null 2>&1; then
    local_exists=1
    if [[ "$skip_existing" -eq 1 ]]; then
      if ! local_tag_commit="$(git rev-parse -q --verify "refs/tags/$tag^{commit}")"; then
        echo "error: local tag $tag does not resolve to a commit" >&2
        exit 1
      fi
      if [[ "$local_tag_commit" != "$head_commit" ]]; then
        echo "error: local tag $tag resolves to $local_tag_commit; --skip-existing requires HEAD $head_commit" >&2
        exit 1
      fi
    fi
  fi

  if [[ "$push" -eq 1 ]]; then
    if inspect_remote_tag "$remote" "$tag"; then
      remote_exists="$REMOTE_TAG_EXISTS"
      remote_tag_commit="$REMOTE_TAG_COMMIT"
    else
      remote_status=$?
      exit "$remote_status"
    fi
    if [[ "$remote_exists" -eq 1 && "$skip_existing" -eq 1 && "$remote_tag_commit" != "$head_commit" ]]; then
      echo "error: remote tag $tag resolves to $remote_tag_commit; --skip-existing requires HEAD $head_commit" >&2
      exit 1
    fi
  fi

  if [[ "$local_exists" -eq 1 ]] || [[ "$remote_exists" -eq 1 ]]; then
    if [[ "$skip_existing" -eq 1 ]]; then
      if [[ "$local_exists" -eq 1 ]] && [[ "$remote_exists" -eq 0 ]] && [[ "$push" -eq 1 ]]; then
        echo "reuse local tag for push: $tag"
        tags_to_push+=("$tag")
      else
        echo "skip existing: $tag"
      fi
      continue
    fi

    if [[ "$local_exists" -eq 1 ]]; then
      echo "error: local tag already exists: $tag" >&2
    else
      echo "error: remote tag already exists on $remote: $tag" >&2
    fi
    exit 1
  fi

  tags_to_create+=("$tag")
  if [[ "$push" -eq 1 ]]; then
    tags_to_push+=("$tag")
  fi
done

if [[ ${#tags_to_create[@]} -eq 0 ]] && [[ ${#tags_to_push[@]} -eq 0 ]]; then
  echo "nothing to do"
  exit 0
fi

echo "repo: $root"
echo "head: $(git rev-parse --short "$head_commit")"
echo "version: $version"
if [[ ${#excludes[@]} -gt 0 ]]; then
  echo "excluded modules: ${excludes[*]}"
fi
if [[ ${#tags_to_create[@]} -gt 0 ]]; then
  echo "create tags (${#tags_to_create[@]}):"
  for t in "${tags_to_create[@]}"; do
    echo "  - $t"
  done
fi
if [[ "$push" -eq 1 ]] && [[ ${#tags_to_push[@]} -gt 0 ]]; then
  echo "push tags (${#tags_to_push[@]}):"
  for t in "${tags_to_push[@]}"; do
    echo "  - $t"
  done
fi

if [[ "$dry_run" -eq 1 ]]; then
  echo "dry-run: no tags created"
  exit 0
fi

current_head="$(git rev-parse --verify HEAD)"
if [[ "$current_head" != "$head_commit" ]]; then
  echo "error: HEAD changed during release planning; expected $head_commit, found $current_head" >&2
  exit 1
fi
if capture_worktree_status "immediately before tag mutation"; then
  if [[ -n "$WORKTREE_STATUS" ]]; then
    echo "error: working tree changed during release planning; refusing to mutate tags" >&2
    exit 1
  fi
else
  exit $?
fi

if [[ ${#tags_to_create[@]} -gt 0 ]]; then
  for t in "${tags_to_create[@]}"; do
    git tag -a "$t" -m "release $t" "$head_commit"
  done
fi

if [[ ${#tags_to_create[@]} -gt 0 ]]; then
  echo "created ${#tags_to_create[@]} tags"
fi

if [[ "$push" -eq 1 ]]; then
  tag_refspecs=()
  for t in "${tags_to_push[@]}"; do
    tag_refspecs+=("refs/tags/$t:refs/tags/$t")
  done
  git push --atomic "$remote" "${tag_refspecs[@]}"
  echo "pushed ${#tags_to_push[@]} tags to $remote"
else
  echo "not pushed (use --push)"
fi
