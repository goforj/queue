#!/usr/bin/env bash
set -euo pipefail

# Runs root and every nested module test suite from a single entrypoint.
#
# Default mode is compile-only (fast) to validate module wiring.
# Set FULL=1 to run full test suites.
# Set VET=1 to run vet after each module's tests.
#
# Examples:
#   ./scripts/test-all-modules.sh
#   FULL=1 ./scripts/test-all-modules.sh
#   FULL=1 VET=1 ./scripts/test-all-modules.sh
#   GOCACHE=/tmp/queue-gocache ./scripts/test-all-modules.sh

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GOCACHE_DIR="${GOCACHE:-/tmp/queue-gocache}"
FULL="${FULL:-0}"
VET="${VET:-0}"

ROOT_TEST_ARGS=("./..." "-count=1")
MODULE_TEST_ARGS=("./..." "-count=1")

if [[ "$FULL" != "1" ]]; then
  ROOT_TEST_ARGS+=("-run" "^$")
  MODULE_TEST_ARGS+=("-run" "^$")
fi

run_root() {
  echo "==> root module"
  (cd "$ROOT_DIR" && GOCACHE="$GOCACHE_DIR" go test "${ROOT_TEST_ARGS[@]}")
  if [[ "$VET" == "1" ]]; then
    (cd "$ROOT_DIR" && GOCACHE="$GOCACHE_DIR" go vet ./...)
  fi
}

run_module() {
  local mod_dir="$1"
  local vet_module="${2:-$VET}"
  echo "==> ${mod_dir} (GOWORK=off)"
  (
    cd "$ROOT_DIR/$mod_dir" && \
      GOWORK=off GOCACHE="$GOCACHE_DIR" go test "${MODULE_TEST_ARGS[@]}"
  )
  if [[ "$vet_module" == "1" ]]; then
    (cd "$ROOT_DIR/$mod_dir" && GOWORK=off GOCACHE="$GOCACHE_DIR" go vet ./...)
  fi
}

run_tooling_module() {
  local mod_dir="$1"
  echo "==> ${mod_dir} tooling module (GOWORK=off)"
  (cd "$ROOT_DIR/$mod_dir" && GOWORK=off GOCACHE="$GOCACHE_DIR" go mod verify)
}

"$ROOT_DIR/scripts/check-module-inventory.sh"
"$ROOT_DIR/scripts/test-release-scripts.sh"
"$ROOT_DIR/scripts/check-generated-docs.sh"
run_root
run_module "driver/redisqueue"
run_module "driver/natsqueue"
run_module "driver/sqsqueue"
run_module "driver/rabbitmqqueue"
run_module "driver/sqlqueuecore"
run_module "driver/mysqlqueue"
run_module "driver/postgresqueue"
run_module "driver/sqlitequeue"
run_module "examples"
run_module "integration"
# The generated-doc check compiles the build-ignored tools; module verification covers their dependency graph.
run_tooling_module "docs"

echo "==> all module tests completed"
