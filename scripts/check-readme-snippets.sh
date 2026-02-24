#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

GOCACHE_DIR="${GOCACHE:-/tmp/queue-gocache}"

GOCACHE="$GOCACHE_DIR" go test ./internal/readmecheck -run '^TestReadmeManualSnippetsCompile$' -count=1
