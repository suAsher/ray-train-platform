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
grep -Fq 'preferredDuringSchedulingIgnoredDuringExecution:' "$values_file" || {
  echo 'Alloy StatefulSet must prefer CPU nodes' >&2
  exit 1
}
grep -Fq 'weight: 100' "$values_file"
grep -Fq 'key: platform.wellspiking.ai/pool' "$values_file"
grep -Fq 'values: [control-plane]' "$values_file"
grep -Fq 'key: node.kubernetes.io/instance-type' "$values_file"
grep -Fq 'values: [virtual-node]' "$values_file"
if grep -A3 'nodeSelector:' "$values_file" | grep -Fq 'platform.wellspiking.ai/pool: control-plane'; then
  echo 'Alloy StatefulSet must not retain a hard control-plane nodeSelector' >&2
  exit 1
fi
if grep -Fq 'nvidia.com/gpu' "$values_file"; then
  echo 'Alloy must not request GPU resources' >&2
  exit 1
fi

rendered="$(mktemp)"
trap 'rm -f "$rendered"' EXIT
helm template "$release" "$chart" --version "$chart_version" --namespace "$namespace" --values "$values_file" >"$rendered"

for required in \
  'kind: StatefulSet' \
  'replicas: 2' \
  'harbor.wellspiking.ai/hub/grafana/alloy' \
  'platform_job_id' \
  'preferredDuringSchedulingIgnoredDuringExecution:' \
  'weight: 100' \
  'key: platform.wellspiking.ai/pool' \
  'key: node.kubernetes.io/instance-type' \
  'virtual-node'; do
  grep -Fq -- "$required" "$rendered" || { echo "Alloy render contract missing: ${required}" >&2; exit 1; }
done
if grep -Fq 'nvidia.com/gpu' "$rendered"; then
  echo 'Alloy must not request GPU resources' >&2
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
