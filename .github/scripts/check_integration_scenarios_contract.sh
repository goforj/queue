#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
repo_root="$(cd -- "${script_dir}/../.." >/dev/null 2>&1 && pwd)"

required_backends=(redis mysql postgres sqlite nats sqs rabbitmq)
required_integration_scenarios=(
  scenario_register_handler
  scenario_start_idempotent
  scenario_enqueue_burst
  scenario_wait_all_processed
  scenario_poison_message_max_retry
  scenario_worker_restart_recovery
  scenario_bind_invalid_json
  scenario_unique_queue_scope
  scenario_enqueue_context_cancellation
  scenario_shutdown_during_delay_retry
  scenario_multi_worker_contention
  scenario_duplicate_delivery_idempotency
  scenario_enqueue_during_broker_fault
  scenario_consume_after_broker_recovery
  scenario_ordering_contract
  scenario_backpressure_saturation
  scenario_payload_large
  scenario_config_option_fuzz
  scenario_shutdown_idempotent
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

echo "Checking required integration scenarios exist in integration suite..."
for scenario in "${required_integration_scenarios[@]}"; do
  if ! has_pattern "t\\.Run\\(\"${scenario}\"" "${repo_root}/integration_scenarios_test.go"; then
    echo "missing integration scenario in integration_scenarios_test.go: ${scenario}"
    exit 1
  fi
done

echo "Checking scenarios document lists required baseline scenarios..."
for scenario in "${required_integration_scenarios[@]}"; do
  if ! has_pattern "- \`${scenario}\`" "${repo_root}/docs/integration-scenarios.md"; then
    echo "missing integration scenario in docs/integration-scenarios.md: ${scenario}"
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

echo "Scenarios contract checks passed."
