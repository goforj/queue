#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: $0 <log-file> <output-md>" >&2
  exit 2
fi

log_file="$1"
out_file="$2"

mkdir -p "$(dirname "$out_file")"

{
  echo "## Scenario Durations"
  echo
  echo "Source log: \`$(basename "$log_file")\`"
  echo
} >"$out_file"

if [[ ! -f "$log_file" ]]; then
  echo "_log file not found_" >>"$out_file"
  exit 0
fi

matches="$(grep -Eo '\[[^]]+\]\[[^]]+\] duration=[^[:space:]]+' "$log_file" || true)"
if [[ -z "$matches" ]]; then
  echo "_no scenario duration lines found_" >>"$out_file"
  exit 0
fi

{
  echo "| Backend | Scenario | Duration |"
  echo "|---|---|---|"
  while IFS= read -r line; do
    backend="$(sed -E 's/^\[([^]]+)\]\[([^]]+)\] duration=([^[:space:]]+)$/\1/' <<<"$line")"
    scenario="$(sed -E 's/^\[([^]]+)\]\[([^]]+)\] duration=([^[:space:]]+)$/\2/' <<<"$line")"
    duration="$(sed -E 's/^\[([^]]+)\]\[([^]]+)\] duration=([^[:space:]]+)$/\3/' <<<"$line")"
    printf '| %s | %s | `%s` |\n' "$backend" "$scenario" "$duration"
  done <<<"$matches"
} >>"$out_file"
