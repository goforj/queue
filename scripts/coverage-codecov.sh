#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

OUTPUT_FILE="${COVERAGE_OUTPUT:-coverage.txt}"
TMP_ROOT="${COVERAGE_TMP_DIR:-/tmp/cache-coverage}"
GOCACHE_DIR="${GOCACHE_DIR:-/tmp/go-build-cache}"
INTEGRATION_BACKEND="${INTEGRATION_BACKEND:-all}"

UNIT_DIR="$TMP_ROOT/unit"
INT_DIR="$TMP_ROOT/integration"
MERGED_DIR="$TMP_ROOT/merged"

rm -rf "$TMP_ROOT"
mkdir -p "$UNIT_DIR" "$INT_DIR" "$MERGED_DIR"

echo "==> Unit coverage"
GOCACHE="$GOCACHE_DIR" \
go test -cover -coverpkg=./... ./... -args -test.gocoverdir="$UNIT_DIR"

echo "==> Integration coverage (backend=${INTEGRATION_BACKEND})"
RUN_INTEGRATION=1 INTEGRATION_BACKEND="$INTEGRATION_BACKEND" GOCACHE="$GOCACHE_DIR" \
go test -cover -tags integration -coverpkg=./... ./... -args -test.gocoverdir="$INT_DIR"

echo "==> Merge coverage"
go tool covdata merge -i="$UNIT_DIR,$INT_DIR" -o="$MERGED_DIR"
go tool covdata textfmt -i="$MERGED_DIR" -o="$OUTPUT_FILE"

go tool cover -func="$OUTPUT_FILE" | tail -n 1
