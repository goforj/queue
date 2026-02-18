#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
repo_root="$(cd -- "${script_dir}/../.." >/dev/null 2>&1 && pwd)"

required_backends=(redis mysql postgres sqlite nats sqs rabbitmq)
required_hardening_steps=(
  step_register_handler
  step_start_idempotent
  step_enqueue_burst
  step_wait_all_processed
  step_poison_message_max_retry
  step_worker_restart_recovery
  step_bind_invalid_json
  step_unique_queue_scope
  step_enqueue_context_cancellation
  step_shutdown_during_delay_retry
  step_multi_worker_contention
  step_duplicate_delivery_idempotency
  step_enqueue_during_broker_fault
  step_consume_after_broker_recovery
  step_ordering_contract
  step_backpressure_saturation
  step_payload_large
  step_config_option_fuzz
  step_shutdown_idempotent
)

has_pattern() {
  local pattern="$1"
  local file="$2"
  if command -v rg >/dev/null 2>&1; then
    rg -q -- "${pattern}" "${file}"
    return
  fi
  grep -Eq -- "${pattern}" "${file}"
}

echo "Checking required hardening steps exist in integration suite..."
for step in "${required_hardening_steps[@]}"; do
  if ! has_pattern "t\\.Run\\(\"${step}\"" "${repo_root}/integration_backends_integration_test.go"; then
    echo "missing hardening step in integration_backends_integration_test.go: ${step}"
    exit 1
  fi
done

echo "Checking hardening document lists required baseline steps..."
for step in "${required_hardening_steps[@]}"; do
  if ! has_pattern "- \`${step}\`" "${repo_root}/docs/hardening.md"; then
    echo "missing hardening step in docs/hardening.md: ${step}"
    exit 1
  fi
done

echo "Checking backend coverage in integration contract suites..."
backend_pattern() {
  local backend="$1"
  printf 'integrationBackendEnabled\("%s"\)|backend:[[:space:]]*"%s"|name:[[:space:]]*"%s"' "${backend}" "${backend}" "${backend}"
}

for backend in "${required_backends[@]}"; do
  if ! has_pattern "$(backend_pattern "${backend}")" "${repo_root}/contract_integration_test.go"; then
    echo "missing backend ${backend} in contract_integration_test.go"
    exit 1
  fi
  if ! has_pattern "$(backend_pattern "${backend}")" "${repo_root}/worker_contract_integration_test.go"; then
    echo "missing backend ${backend} in worker_contract_integration_test.go"
    exit 1
  fi
  if ! has_pattern "$(backend_pattern "${backend}")" "${repo_root}/observability_integration_test.go"; then
    echo "missing backend ${backend} in observability_integration_test.go"
    exit 1
  fi
done

echo "Checking required observability integration contract tests exist..."
if ! has_pattern "func TestObservabilityIntegration_AllBackends\\(" "${repo_root}/observability_integration_test.go"; then
  echo "missing TestObservabilityIntegration_AllBackends"
  exit 1
fi
if ! has_pattern "func TestObservabilityIntegration_PauseResumeSupport_AllBackends\\(" "${repo_root}/observability_integration_test.go"; then
  echo "missing TestObservabilityIntegration_PauseResumeSupport_AllBackends"
  exit 1
fi

echo "Hardening contract checks passed."
