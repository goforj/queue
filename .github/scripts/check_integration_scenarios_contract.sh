#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
repo_root="$(cd -- "${script_dir}/../.." >/dev/null 2>&1 && pwd)"
integration_root="${repo_root}/integration"
integration_all_file="${integration_root}/all/integration_scenarios_test.go"
integration_contract_file="${integration_root}/root/contract_integration_test.go"
integration_observability_file="${integration_root}/root/observability_integration_test.go"

required_backends=(redis mysql postgres sqlite nats sqs rabbitmq)
required_integration_scenarios=(
  scenario_register_handler
  scenario_startworkers_idempotent
  scenario_dispatch_burst
  scenario_wait_all_processed
  scenario_poison_message_max_retry
  scenario_worker_restart_recovery
  scenario_bind_invalid_json
  scenario_unique_queue_scope
  scenario_dispatch_context_cancellation
  scenario_shutdown_during_delay_retry
  scenario_multi_worker_contention
  scenario_duplicate_delivery_idempotency
  scenario_dispatch_during_broker_fault
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
  if ! has_pattern "t\\.Run\\(\"${scenario}\"" "${integration_all_file}"; then
    echo "missing integration scenario in ${integration_all_file}: ${scenario}"
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
  local backend_const=""
  case "${backend}" in
    redis) backend_const="BackendRedis" ;;
    mysql) backend_const="BackendMySQL" ;;
    postgres) backend_const="BackendPostgres" ;;
    sqlite) backend_const="BackendSQLite" ;;
    nats) backend_const="BackendNATS" ;;
    sqs) backend_const="BackendSQS" ;;
    rabbitmq) backend_const="BackendRabbitMQ" ;;
    *) backend_const="" ;;
  esac

  if [[ -n "${backend_const}" ]]; then
    printf 'integrationBackendEnabled\("%s"\)|integrationBackendEnabled\(testenv\.%s\)|backend:[[:space:]]*"%s"|backend:[[:space:]]*testenv\.%s|name:[[:space:]]*"%s"|name:[[:space:]]*testenv\.%s' "${backend}" "${backend_const}" "${backend}" "${backend_const}" "${backend}" "${backend_const}"
    return
  fi

  printf 'integrationBackendEnabled\("%s"\)|backend:[[:space:]]*"%s"|name:[[:space:]]*"%s"' "${backend}" "${backend}" "${backend}"
}

for backend in "${required_backends[@]}"; do
  if ! has_pattern "$(backend_pattern "${backend}")" "${integration_contract_file}"; then
    echo "missing backend ${backend} in ${integration_contract_file}"
    exit 1
  fi
  if ! has_pattern "$(backend_pattern "${backend}")" "${integration_observability_file}"; then
    echo "missing backend ${backend} in ${integration_observability_file}"
    exit 1
  fi
done

echo "Checking required observability integration contract tests exist..."
if ! has_pattern "func TestObservabilityIntegration_AllBackends\\(" "${integration_observability_file}"; then
  echo "missing TestObservabilityIntegration_AllBackends"
  exit 1
fi
if ! has_pattern "func TestObservabilityIntegration_PauseResumeSupport_AllBackends\\(" "${integration_observability_file}"; then
  echo "missing TestObservabilityIntegration_PauseResumeSupport_AllBackends"
  exit 1
fi

echo "Scenarios contract checks passed."
