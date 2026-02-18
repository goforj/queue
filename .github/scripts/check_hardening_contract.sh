#!/usr/bin/env bash
set -euo pipefail

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

echo "Checking required hardening steps exist in integration suite..."
for step in "${required_hardening_steps[@]}"; do
  if ! rg -q "t\\.Run\\(\"${step}\"" integration_backends_integration_test.go; then
    echo "missing hardening step in integration_backends_integration_test.go: ${step}"
    exit 1
  fi
done

echo "Checking hardening document lists required baseline steps..."
for step in "${required_hardening_steps[@]}"; do
  if ! rg -q -- "- \`${step}\`" hardening.md; then
    echo "missing hardening step in hardening.md: ${step}"
    exit 1
  fi
done

echo "Checking backend coverage in integration contract suites..."
backend_pattern() {
  local backend="$1"
  printf 'integrationBackendEnabled\("%s"\)|backend:\s*"%s"|name:\s*"%s"' "${backend}" "${backend}" "${backend}"
}

for backend in "${required_backends[@]}"; do
  if ! rg -q "$(backend_pattern "${backend}")" contract_integration_test.go; then
    echo "missing backend ${backend} in contract_integration_test.go"
    exit 1
  fi
  if ! rg -q "$(backend_pattern "${backend}")" worker_contract_integration_test.go; then
    echo "missing backend ${backend} in worker_contract_integration_test.go"
    exit 1
  fi
  if ! rg -q "$(backend_pattern "${backend}")" observability_integration_test.go; then
    echo "missing backend ${backend} in observability_integration_test.go"
    exit 1
  fi
done

echo "Checking required observability integration contract tests exist..."
if ! rg -q "func TestObservabilityIntegration_AllBackends\\(" observability_integration_test.go; then
  echo "missing TestObservabilityIntegration_AllBackends"
  exit 1
fi
if ! rg -q "func TestObservabilityIntegration_PauseResumeSupport_AllBackends\\(" observability_integration_test.go; then
  echo "missing TestObservabilityIntegration_PauseResumeSupport_AllBackends"
  exit 1
fi

echo "Hardening contract checks passed."
