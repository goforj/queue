#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
VERSION=""
EXCLUDES=()
VERSION_VALIDATOR="$ROOT_DIR/scripts/release-version.sh"

if [[ ! -r "$VERSION_VALIDATOR" ]]; then
  echo "error: release version validator is missing or unreadable: $VERSION_VALIDATOR" >&2
  exit 1
fi
# shellcheck source=scripts/release-version.sh
source "$VERSION_VALIDATOR"

usage() {
  cat <<'USAGE'
Usage:
  scripts/plan-module-release-tags.sh <version> [--exclude <module-dir>]...

Prints the release tag for every included Go module without inspecting or
mutating Git tags.
USAGE
}

normalize_module_dir() {
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

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    --exclude)
      excluded="${2:-}"
      if [[ -z "$excluded" ]]; then
        echo "error: --exclude requires a module directory value" >&2
        exit 1
      fi
      EXCLUDES+=("$(normalize_module_dir "$excluded")")
      shift 2
      ;;
    v*)
      if [[ -n "$VERSION" ]]; then
        echo "error: multiple versions provided" >&2
        exit 1
      fi
      VERSION="$1"
      shift
      ;;
    *)
      echo "error: unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

if [[ -z "$VERSION" ]]; then
  echo "error: version is required (example: v0.1.3)" >&2
  exit 1
fi

if ! validate_release_version "$VERSION"; then
  echo "error: $RELEASE_VERSION_ERROR" >&2
  exit 1
fi

module_count=0
while IFS= read -r discovered_dir; do
  module_count=$((module_count + 1))
  dir="$(normalize_module_dir "$discovered_dir")"
  if module_is_excluded "$dir"; then
    continue
  fi

  relative_file="go.mod"
  if [[ "$dir" != "." ]]; then
    relative_file="$dir/go.mod"
  fi

  module_path="$(awk '$1 == "module" { value = $2; gsub(/^"|"$/, "", value); print value; exit }' "$ROOT_DIR/$relative_file")"
  if [[ -z "$module_path" ]]; then
    echo "error: could not read module path from $relative_file" >&2
    exit 1
  fi
  if ! validate_release_module_path "$module_path" "$VERSION"; then
    echo "error: $RELEASE_VERSION_ERROR" >&2
    exit 1
  fi

  if [[ "$dir" == "." ]]; then
    printf '%s\n' "$VERSION"
  else
    printf '%s/%s\n' "$dir" "$VERSION"
  fi
done < <(
  cd "$ROOT_DIR"
  find . -name go.mod -type f \
    -not -path './.git/*' \
    -not -path './*/.git/*' \
    -not -path './*/vendor/*' \
    -exec dirname {} \; | sed 's#^\./##' | sort
)

if [[ "$module_count" -eq 0 ]]; then
  echo "error: no modules discovered" >&2
  exit 1
fi
