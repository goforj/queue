#!/usr/bin/env bash
set -euo pipefail

# Runs integration tests with practical defaults.
#
# Defaults:
#   INTEGRATION_BACKEND=all
#   GOCACHE=<shell default> (unless explicitly set in env)
#   INTEGRATION_PKG=./integration/...
#
# Examples:
#   ./scripts/test-integration.sh
#   INTEGRATION_BACKEND=redis ./scripts/test-integration.sh
#   INTEGRATION_BACKEND=all ./scripts/test-integration.sh -run TestObservabilityIntegration_ProcessEvents_AllBackends -v

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKEND="${INTEGRATION_BACKEND:-all}"
PKG="${INTEGRATION_PKG:-./integration/...}"

cd "$ROOT_DIR"
echo "==> running integration tests"
echo "    backend: $BACKEND"
echo "    package: $PKG"
if [[ -n "${GOCACHE:-}" ]]; then
  echo "    gocache: $GOCACHE"
  INTEGRATION_BACKEND="$BACKEND" GOCACHE="$GOCACHE" \
    go test -tags integration "$PKG" -count=1 "$@"
else
  echo "    gocache: <shell default>"
  INTEGRATION_BACKEND="$BACKEND" \
    go test -tags integration "$PKG" -count=1 "$@"
fi
