#!/usr/bin/env bash
set -euo pipefail

readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly namespace="${LOKI_NAMESPACE:-loki}"
readonly release="${LOKI_RELEASE:-loki}"
readonly chart="${LOKI_CHART:-/opt/guofeng/vke-cluster/loki/loki-18.7.5.tgz}"
readonly values_file="${LOKI_VALUES_FILE:-${script_dir}/20-values-vci-ha.yaml}"
readonly tos_secret="${LOKI_TOS_SECRET:-loki-tos}"
mode="${1:---render-only}"

usage() {
  cat <<'EOF'
Usage: deploy-vci-ha.sh [--render-only|--install]

Environment:
  LOKI_NAMESPACE        Target namespace (default: loki)
  LOKI_RELEASE          Helm release name (default: loki)
  LOKI_CHART            Local Loki chart archive
  LOKI_VALUES_FILE      Production values file
  LOKI_TOS_SECRET       Existing TOS credential Secret (default: loki-tos)
  CLEANUP_LEGACY_PVCS   Set to true to remove only the three confirmed old,
                        Pending, unowned SimpleScalable PVCs before install.
EOF
}

if [[ "$mode" != "--render-only" && "$mode" != "--install" ]]; then
  usage >&2
  exit 2
fi

for required in helm kubectl grep; do
  command -v "$required" >/dev/null || { echo "missing required command: $required" >&2; exit 1; }
done
[[ -f "$chart" ]] || { echo "Loki chart not found: $chart" >&2; exit 1; }
[[ -f "$values_file" ]] || { echo "Loki values not found: $values_file" >&2; exit 1; }

rendered="$(mktemp)"
trap 'rm -f "$rendered"' EXIT
helm template "$release" "$chart" --namespace "$namespace" --values "$values_file" >"$rendered"

require_rendered() {
  local pattern="$1"
  grep -Fq -- "$pattern" "$rendered" || { echo "Loki render contract missing: $pattern" >&2; exit 1; }
}

require_rendered 'image: harbor.wellspiking.ai/hub/grafana/loki:3.7.6'
require_rendered 'image: harbor.wellspiking.ai/hub/nginxinc/nginx-unprivileged:1.31-alpine'
require_rendered 'name: initialize-wal-permissions'
require_rendered 'storageClassName: ebs-ssd'
require_rendered 'node.kubernetes.io/instance-type'
require_rendered '- virtual-node'
require_rendered 'name: storage'
require_rendered 'mountPath: /var/loki'

if grep -Eq '(TOS_SECRET_ACCESS_KEY|TOS_ACCESS_KEY_ID): [^$]' "$rendered"; then
  echo 'rendered manifest contains a literal TOS credential' >&2
  exit 1
fi

echo 'Loki render contract verified'
if [[ "$mode" == "--render-only" ]]; then
  exit 0
fi

kubectl get namespace "$namespace" >/dev/null 2>&1 || kubectl create namespace "$namespace"
kubectl -n "$namespace" get secret "$tos_secret" >/dev/null
for key in TOS_ACCESS_KEY_ID TOS_SECRET_ACCESS_KEY; do
  present="$(kubectl -n "$namespace" get secret "$tos_secret" -o "go-template={{if index .data \"${key}\"}}present{{end}}")"
  [[ "$present" == "present" ]] || { echo "Secret ${tos_secret} is missing ${key}" >&2; exit 1; }
done

legacy_claims=(data-loki-write-0 data-loki-write-1 data-loki-write-2)
if [[ "${CLEANUP_LEGACY_PVCS:-false}" == "true" ]]; then
  for claim in "${legacy_claims[@]}"; do
    if ! kubectl -n "$namespace" get pvc "$claim" >/dev/null 2>&1; then
      continue
    fi
    phase="$(kubectl -n "$namespace" get pvc "$claim" -o jsonpath='{.status.phase}')"
    owners="$(kubectl -n "$namespace" get pvc "$claim" -o jsonpath='{range .metadata.ownerReferences[*]}{.uid}{end}')"
    [[ "$phase" == "Pending" && -z "$owners" ]] || {
      echo "refusing to delete legacy PVC ${claim}: phase=${phase:-unknown}, has_owner=$([[ -n "$owners" ]] && echo yes || echo no)" >&2
      exit 1
    }
  done
  kubectl -n "$namespace" delete pvc "${legacy_claims[@]}" --ignore-not-found
fi

helm upgrade --install "$release" "$chart" \
  --namespace "$namespace" \
  --values "$values_file" \
  --atomic \
  --timeout 15m
