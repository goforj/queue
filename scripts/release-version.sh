#!/usr/bin/env bash

# shellcheck disable=SC2034 # Callers consume these diagnostics after sourcing this file.

RELEASE_VERSION_ERROR=""
RELEASE_VERSION_MAJOR=""

validate_release_version() {
  local version="$1"
  local without_build
  local prerelease
  local identifier
  local -a prerelease_identifiers=()

  RELEASE_VERSION_ERROR=""
  RELEASE_VERSION_MAJOR=""
  if [[ ! "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-([0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*))?(\+([0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*))?$ ]]; then
    RELEASE_VERSION_ERROR="invalid Go release version $version: expected vMAJOR.MINOR.PATCH with valid SemVer prerelease/build identifiers"
    return 1
  fi

  RELEASE_VERSION_MAJOR="${BASH_REMATCH[1]}"
  without_build="${version%%+*}"
  if [[ "$without_build" == *-* ]]; then
    prerelease="${without_build#*-}"
    IFS='.' read -r -a prerelease_identifiers <<<"$prerelease"
    for identifier in "${prerelease_identifiers[@]}"; do
      if [[ "$identifier" =~ ^[0-9]+$ && "$identifier" == 0[0-9]* ]]; then
        RELEASE_VERSION_ERROR="invalid Go release version $version: numeric prerelease identifier $identifier must not contain leading zeroes"
        return 1
      fi
    done
  fi
}

validate_release_module_path() {
  local module_path="$1"
  local version="$2"
  local version_major
  local path_major

  if ! validate_release_version "$version"; then
    return 1
  fi
  version_major="$RELEASE_VERSION_MAJOR"

  if [[ "$module_path" == gopkg.in/* ]]; then
    if [[ ! "$module_path" =~ \.v(0|[1-9][0-9]*)$ ]]; then
      RELEASE_VERSION_ERROR="module path $module_path requires .v$version_major for release $version"
      return 1
    fi
    path_major="${BASH_REMATCH[1]}"
    if [[ "$path_major" != "$version_major" ]]; then
      RELEASE_VERSION_ERROR="module path $module_path declares major v$path_major but release $version uses v$version_major"
      return 1
    fi
    return 0
  fi

  if [[ "$module_path" =~ /v([0-9]+)$ ]]; then
    path_major="${BASH_REMATCH[1]}"
    if [[ "$path_major" == 0* || "$path_major" == "1" ]]; then
      RELEASE_VERSION_ERROR="module path $module_path has an invalid semantic import suffix for release $version"
      return 1
    fi
    if [[ "$path_major" != "$version_major" ]]; then
      RELEASE_VERSION_ERROR="module path $module_path declares major v$path_major but release $version uses v$version_major"
      return 1
    fi
    return 0
  fi

  if [[ "$version_major" != "0" && "$version_major" != "1" ]]; then
    RELEASE_VERSION_ERROR="module path $module_path requires /v$version_major for release $version"
    return 1
  fi
}
