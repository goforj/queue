#!/usr/bin/env bash
set -euo pipefail

# Runs root + driver module tests from a single entrypoint.
#
# Default mode is compile-only (fast) to validate module wiring.
# Set FULL=1 to run full test suites.
#
# Examples:
#   ./scripts/test-all-modules.sh
#   FULL=1 ./scripts/test-all-modules.sh
#   GOCACHE=/tmp/queue-gocache ./scripts/test-all-modules.sh

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GOCACHE_DIR="${GOCACHE:-/tmp/queue-gocache}"
FULL="${FULL:-0}"

ROOT_TEST_ARGS=("./..." "-count=1")
MODULE_TEST_ARGS=("./..." "-count=1")

if [[ "$FULL" != "1" ]]; then
  ROOT_TEST_ARGS+=("-run" "^$")
  MODULE_TEST_ARGS+=("-run" "^$")
fi

run_root() {
  echo "==> root module"
  (cd "$ROOT_DIR" && GOCACHE="$GOCACHE_DIR" go test "${ROOT_TEST_ARGS[@]}")
}

run_driver_module() {
  local mod_dir="$1"
  echo "==> ${mod_dir} (GOWORK=off)"
  (
    cd "$ROOT_DIR/$mod_dir" && \
      GOWORK=off GOCACHE="$GOCACHE_DIR" go test "${MODULE_TEST_ARGS[@]}"
  )
}

run_root
run_driver_module "driver/redisqueue"
run_driver_module "driver/natsqueue"
run_driver_module "driver/sqsqueue"
run_driver_module "driver/rabbitmqqueue"
run_driver_module "driver/sqlqueuecore"
run_driver_module "driver/mysqlqueue"
run_driver_module "driver/postgresqueue"
run_driver_module "driver/sqlitequeue"
run_driver_module "examples"
run_driver_module "integration"

echo "==> all module tests completed"
