#!/usr/bin/env bash
set -euo pipefail

readonly namespace="${ALLOY_NAMESPACE:-monitoring}"
readonly release="${ALLOY_RELEASE:-alloy}"
readonly chart="${ALLOY_CHART:-grafana/alloy}"
readonly chart_version="${ALLOY_CHART_VERSION:-1.11.1}"
readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly values_file="${ALLOY_VALUES_FILE:-${script_dir}/20-values-cpu-ha.yaml}"
readonly mode="${1:---render-only}"

if [[ "$mode" != '--render-only' && "$mode" != '--install' ]]; then
  echo 'usage: deploy-ha.sh [--render-only|--install]' >&2
  exit 2
fi

for required in helm kubectl grep; do
  command -v "$required" >/dev/null || { echo "missing required command: ${required}" >&2; exit 1; }
done
[[ -f "$values_file" ]] || { echo "Alloy values not found: ${values_file}" >&2; exit 1; }

rendered="$(mktemp)"
trap 'rm -f "$rendered"' EXIT
helm template "$release" "$chart" --version "$chart_version" --namespace "$namespace" --values "$values_file" >"$rendered"

for required in \
  'kind: StatefulSet' \
  'replicas: 2' \
  'harbor.wellspiking.ai/hub/grafana/alloy' \
  'platform_job_id' \
  'platform.wellspiking.ai/pool: control-plane'; do
  grep -Fq -- "$required" "$rendered" || { echo "Alloy render contract missing: ${required}" >&2; exit 1; }
done
if grep -Fq 'virtual-node' "$rendered" || grep -Fq 'burst-to-vci' "$rendered"; then
  echo 'CPU Alloy profile must not target VCI' >&2
  exit 1
fi

echo 'Alloy CPU HA render contract verified'
if [[ "$mode" == '--render-only' ]]; then
  exit 0
fi

helm upgrade --install "$release" "$chart" \
  --version "$chart_version" \
  --namespace "$namespace" \
  --values "$values_file" \
  --wait --timeout 10m
