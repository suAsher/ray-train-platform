#!/usr/bin/env bash
set -euo pipefail

readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly namespace="${LOKI_NAMESPACE:-loki}"
readonly old_release="${LOKI_OLD_RELEASE:-loki}"
readonly new_release="${LOKI_NEW_RELEASE:-loki-cpu}"
readonly chart="${LOKI_CHART:-/opt/guofeng/vke-cluster/loki/loki-18.7.5.tgz}"
readonly old_values="${LOKI_OLD_VALUES:-${script_dir}/20-values-vci-ha.yaml}"
readonly mode="${1:---dry-run}"

if [[ "$mode" != '--dry-run' && "$mode" != '--execute' ]]; then
  echo 'usage: migrate-vci-to-cpu.sh [--dry-run|--execute]' >&2
  exit 2
fi

for required in helm kubectl bash; do
  command -v "$required" >/dev/null || { echo "missing required command: ${required}" >&2; exit 1; }
done
[[ -f "$chart" ]] || { echo "Loki chart not found: ${chart}" >&2; exit 1; }
[[ -f "$old_values" ]] || { echo "Old Loki values not found: ${old_values}" >&2; exit 1; }

echo "old release retained: ${old_release}; new CPU release: ${new_release}"
echo 'old Loki PVCs remain intact; the operation does not remove historical WAL volumes'
if [[ "$mode" == '--dry-run' ]]; then
  exit 0
fi

LOKI_RELEASE="$old_release" LOKI_GATEWAY_SERVICE="${old_release}-gateway" LOKI_EXPECTED_INSTANCE_TYPE=virtual-node \
  bash "${script_dir}/30-verify-loki.sh"

helm upgrade "$old_release" "$chart" \
  --namespace "$namespace" \
  --values "$old_values" \
  --set singleBinary.podDisruptionBudget.enabled=false \
  --set gateway.podDisruptionBudget.enabled=false \
  --set singleBinary.replicas=0 \
  --set gateway.replicas=0 \
  --wait --timeout 15m

LOKI_RELEASE="$new_release" bash "${script_dir}/deploy-ha.sh" --install
LOKI_RELEASE="$new_release" LOKI_GATEWAY_SERVICE="${new_release}-gateway" LOKI_EXPECTED_NODE_POOL=control-plane \
  bash "${script_dir}/30-verify-loki.sh"
