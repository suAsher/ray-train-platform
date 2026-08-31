#!/usr/bin/env bash
# Verify that a real GPU worker can mount every administrator-configured IDC
# NFS export read-only. This script is intentionally destructive only to its
# uniquely named temporary namespace, which it removes on exit.
set -euo pipefail

for command in kubectl jq envsubst; do
  command -v "$command" >/dev/null 2>&1 || { printf 'missing required command: %s\n' "$command" >&2; exit 2; }
done

sources="${IDC_DATA_SPACES_SOURCES_JSON:-}"
selector="${TRAINING_NODE_SELECTOR:-}"
[[ -n "$sources" ]] || { printf '%s\n' 'IDC_DATA_SPACES_SOURCES_JSON is required' >&2; exit 2; }
[[ -n "$selector" ]] || { printf '%s\n' 'TRAINING_NODE_SELECTOR is required' >&2; exit 2; }

for name in original wellspiking shared spk-hybrid spk-ssd; do
  jq -e --arg name "$name" '.[$name] | .server | strings | select(length > 0)' <<<"$sources" >/dev/null || { printf 'IDC source %s.server is required\n' "$name" >&2; exit 2; }
  jq -e --arg name "$name" '.[$name] | .path | strings | select(startswith("/") and . != "/")' <<<"$sources" >/dev/null || { printf 'IDC source %s.path is invalid\n' "$name" >&2; exit 2; }
done

selector_json='{}'
IFS=',' read -r -a selector_parts <<<"$selector"
for part in "${selector_parts[@]}"; do
  [[ "$part" =~ ^([A-Za-z0-9./_-]+)=([A-Za-z0-9._-]+)$ ]] || { printf 'invalid training node selector: %s\n' "$selector" >&2; exit 2; }
  key="${BASH_REMATCH[1]}"
  value="${BASH_REMATCH[2]}"
  selector_json="$(jq -cn --argjson current "$selector_json" --arg key "$key" --arg value "$value" '$current + {($key): $value}')"
done

export IDC_NFS_ORIGINAL_SERVER="$(jq -r '.original.server' <<<"$sources")"
export IDC_NFS_ORIGINAL_PATH="$(jq -r '.original.path' <<<"$sources")"
export IDC_NFS_WELLSPIKING_SERVER="$(jq -r '.wellspiking.server' <<<"$sources")"
export IDC_NFS_WELLSPIKING_PATH="$(jq -r '.wellspiking.path' <<<"$sources")"
export IDC_NFS_SHARED_SERVER="$(jq -r '.shared.server' <<<"$sources")"
export IDC_NFS_SHARED_PATH="$(jq -r '.shared.path' <<<"$sources")"
export IDC_NFS_SPK_HYBRID_SERVER="$(jq -r '.["spk-hybrid"].server' <<<"$sources")"
export IDC_NFS_SPK_HYBRID_PATH="$(jq -r '.["spk-hybrid"].path' <<<"$sources")"
export IDC_NFS_SPK_SSD_SERVER="$(jq -r '.["spk-ssd"].server' <<<"$sources")"
export IDC_NFS_SPK_SSD_PATH="$(jq -r '.["spk-ssd"].path' <<<"$sources")"
export IDC_SMOKE_NAME="ray-idc-nfs-$(date +%s)"
export IDC_SMOKE_NAMESPACE="$IDC_SMOKE_NAME"
export IDC_SMOKE_NODE_SELECTOR_JSON="$selector_json"
export IDC_SMOKE_IMAGE="${IDC_SMOKE_IMAGE:-docker.m.daocloud.io/library/busybox:1.36}"

manifest_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
rendered="$(mktemp)"
cleanup() {
  kubectl delete namespace "$IDC_SMOKE_NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  rm -f "$rendered"
}
trap cleanup EXIT

kubectl create namespace "$IDC_SMOKE_NAMESPACE" >/dev/null
envsubst <"$manifest_dir/40-readonly-mount-smoke.yaml" >"$rendered"
kubectl apply -f "$rendered" >/dev/null
kubectl -n "$IDC_SMOKE_NAMESPACE" wait --for=jsonpath='{.status.phase}'=Succeeded "pod/$IDC_SMOKE_NAME" --timeout=8m
kubectl -n "$IDC_SMOKE_NAMESPACE" logs "$IDC_SMOKE_NAME" | grep -Fxq 'idc-readonly-mount-contract-verified'
printf '%s\n' 'IDC read-only NFS mount contract verified'
