#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GOCACHE_DIR="${GOCACHE:-/tmp/queue-gocache}"
GOMODCACHE_DIR="${GOMODCACHE:-/tmp/queue-gomodcache}"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/queue-generated-docs.XXXXXX")"

cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

run_generators() {
  (
    cd "$ROOT_DIR/docs"
    GOWORK=off GOCACHE="$GOCACHE_DIR" GOMODCACHE="$GOMODCACHE_DIR" go run ./readme/main.go
    GOWORK=off GOCACHE="$GOCACHE_DIR" GOMODCACHE="$GOMODCACHE_DIR" go run ./examplegen/main.go
    TESTCOUNT_USE_INTEGRATION_MANIFEST=1 GOWORK=off GOCACHE="$GOCACHE_DIR" GOMODCACHE="$GOMODCACHE_DIR" go run ./readme/testcounts/main.go
    BENCH_RENDER_ONLY=1 GOWORK=off GOCACHE="$GOCACHE_DIR" GOMODCACHE="$GOMODCACHE_DIR" go test -tags=benchrender ./bench -run '^TestRenderBenchmarks$' -count=1
  )
}

snapshot_outputs() {
  local destination="$1"
  mkdir -p "$destination"
  cp "$ROOT_DIR/README.md" "$destination/README.md"
  cp -R "$ROOT_DIR/examples" "$destination/examples"
  mkdir -p "$destination/bench"
  cp "$ROOT_DIR/docs/bench/benchmarks_rows.json" "$destination/bench/benchmarks_rows.json"
  cp "$ROOT_DIR/docs/bench/benchmarks_ns.svg" "$destination/bench/benchmarks_ns.svg"
  cp "$ROOT_DIR/docs/bench/benchmarks_ops.svg" "$destination/bench/benchmarks_ops.svg"
  cp "$ROOT_DIR/docs/bench/benchmarks_bytes.svg" "$destination/bench/benchmarks_bytes.svg"
  cp "$ROOT_DIR/docs/bench/benchmarks_allocs.svg" "$destination/bench/benchmarks_allocs.svg"
  mkdir -p "$destination/testcounts"
  cp "$ROOT_DIR/docs/readme/testcounts/integration_count.json" "$destination/testcounts/integration_count.json"
}

echo "==> test test-count evidence validation"
(
  cd "$ROOT_DIR/docs"
  GOWORK=off GOCACHE="$GOCACHE_DIR" GOMODCACHE="$GOMODCACHE_DIR" go test -tags=testcounts ./readme/testcounts -count=1
)

snapshot_outputs "$TMP_DIR/before"

echo "==> generate README, examples, test counts, and benchmark dashboard"
run_generators

first_generation_clean=1
if ! diff -u "$TMP_DIR/before/README.md" "$ROOT_DIR/README.md"; then
  first_generation_clean=0
fi
if ! diff -ruN "$TMP_DIR/before/examples" "$ROOT_DIR/examples"; then
  first_generation_clean=0
fi
for output in benchmarks_rows.json benchmarks_ns.svg benchmarks_ops.svg benchmarks_bytes.svg benchmarks_allocs.svg; do
  if ! diff -u "$TMP_DIR/before/bench/$output" "$ROOT_DIR/docs/bench/$output"; then
    first_generation_clean=0
  fi
done
if ! diff -u "$TMP_DIR/before/testcounts/integration_count.json" "$ROOT_DIR/docs/readme/testcounts/integration_count.json"; then
  first_generation_clean=0
fi
if [[ "$first_generation_clean" != "1" ]]; then
  echo "generated documentation differs from the checked-in output"
fi

snapshot_outputs "$TMP_DIR/first"

echo "==> regenerate generated documentation to verify idempotency"
run_generators

idempotent=1
if ! diff -ruN "$TMP_DIR/first/README.md" "$ROOT_DIR/README.md"; then
  idempotent=0
fi
if ! diff -ruN "$TMP_DIR/first/examples" "$ROOT_DIR/examples"; then
  idempotent=0
fi
for output in benchmarks_rows.json benchmarks_ns.svg benchmarks_ops.svg benchmarks_bytes.svg benchmarks_allocs.svg; do
  if ! diff -u "$TMP_DIR/first/bench/$output" "$ROOT_DIR/docs/bench/$output"; then
    idempotent=0
  fi
done
if ! diff -u "$TMP_DIR/first/testcounts/integration_count.json" "$ROOT_DIR/docs/readme/testcounts/integration_count.json"; then
  idempotent=0
fi

if [[ "$first_generation_clean" != "1" || "$idempotent" != "1" ]]; then
  if [[ "$idempotent" != "1" ]]; then
    echo "documentation generators are not idempotent"
  fi
  exit 1
fi

echo "==> generated documentation is current and idempotent"
