#!/usr/bin/env bash
set -euo pipefail

readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly namespace="${LOKI_NAMESPACE:-loki}"
readonly release="${LOKI_RELEASE:-loki-cpu}"
readonly chart="${LOKI_CHART:-/opt/guofeng/vke-cluster/loki/loki-18.7.5.tgz}"
readonly values_file="${LOKI_VALUES_FILE:-${script_dir}/20-values-cpu-ha.yaml}"
readonly tos_secret="${LOKI_TOS_SECRET:-loki-tos}"
readonly mode="${1:---render-only}"

if [[ "$mode" != '--render-only' && "$mode" != '--install' ]]; then
  echo 'usage: deploy-ha.sh [--render-only|--install]' >&2
  exit 2
fi

for required in helm kubectl grep; do
  command -v "$required" >/dev/null || { echo "missing required command: ${required}" >&2; exit 1; }
done
[[ -f "$chart" ]] || { echo "Loki chart not found: ${chart}" >&2; exit 1; }
[[ -f "$values_file" ]] || { echo "Loki values not found: ${values_file}" >&2; exit 1; }

rendered="$(mktemp)"
trap 'rm -f "$rendered"' EXIT
helm template "$release" "$chart" --namespace "$namespace" --values "$values_file" >"$rendered"

require_rendered() {
  local expected="$1"
  grep -Fq -- "$expected" "$rendered" || { echo "Loki render contract missing: ${expected}" >&2; exit 1; }
}

require_rendered 'image: harbor.wellspiking.ai/hub/grafana/loki:3.7.6'
require_rendered 'image: harbor.wellspiking.ai/hub/nginxinc/nginx-unprivileged:1.31-alpine'
require_rendered 'preferredDuringSchedulingIgnoredDuringExecution:'
require_rendered 'weight: 100'
require_rendered 'key: platform.wellspiking.ai/pool'
require_rendered 'control-plane'
require_rendered 'key: node.kubernetes.io/instance-type'
require_rendered 'virtual-node'
require_rendered 'storageClassName: ebs-ssd'
require_rendered 'name: storage'
require_rendered 'mountPath: /var/loki'
if grep -Fq 'nvidia.com/gpu' "$rendered"; then
  echo 'Loki must not request GPU resources' >&2
  exit 1
fi

echo 'Loki CPU HA render contract verified'
if [[ "$mode" == '--render-only' ]]; then
  exit 0
fi

kubectl get namespace "$namespace" >/dev/null 2>&1 || kubectl create namespace "$namespace"
kubectl -n "$namespace" get secret "$tos_secret" >/dev/null
for key in TOS_ACCESS_KEY_ID TOS_SECRET_ACCESS_KEY; do
  present="$(kubectl -n "$namespace" get secret "$tos_secret" -o "go-template={{if index .data \"${key}\"}}present{{end}}")"
  [[ "$present" == 'present' ]] || { echo "Secret ${tos_secret} is missing ${key}" >&2; exit 1; }
done

helm upgrade --install "$release" "$chart" \
  --namespace "$namespace" \
  --values "$values_file" \
  --atomic \
  --timeout 15m
