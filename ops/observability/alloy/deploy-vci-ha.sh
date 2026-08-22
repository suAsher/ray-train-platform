#!/usr/bin/env bash
set -euo pipefail

readonly namespace="${ALLOY_NAMESPACE:-monitoring}"
readonly release="${ALLOY_RELEASE:-alloy}"
readonly chart="${ALLOY_CHART:-grafana/alloy}"
readonly chart_version="${ALLOY_CHART_VERSION:-1.11.1}"
readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly values_file="${ALLOY_VALUES_FILE:-${script_dir}/20-values-vci-ha.yaml}"

for command in helm kubectl grep; do
  command -v "$command" >/dev/null || { echo "missing required command: $command" >&2; exit 1; }
done
[[ -f "$values_file" ]] || { echo "Alloy values not found: $values_file" >&2; exit 1; }

rendered="$(mktemp)"
trap 'rm -f "$rendered"' EXIT
helm template "$release" "$chart" --version "$chart_version" --namespace "$namespace" --values "$values_file" >"$rendered"

for required in \
  'kind: Deployment' \
  'replicas: 2' \
  'harbor.wellspiking.ai/hub/grafana/alloy' \
  'platform_job_id' \
  'node.kubernetes.io/instance-type' \
  'virtual-node'; do
  grep -Fq -- "$required" "$rendered" || { echo "Alloy render contract missing: $required" >&2; exit 1; }
done

echo 'Alloy render contract verified'
if [[ "${1:---render-only}" == '--render-only' ]]; then
  exit 0
fi
[[ "${1:-}" == '--install' ]] || { echo 'usage: deploy-vci-ha.sh [--render-only|--install]' >&2; exit 2; }

helm upgrade --install "$release" "$chart" \
  --version "$chart_version" \
  --namespace "$namespace" \
  --values "$values_file" \
  --wait --timeout 10m
