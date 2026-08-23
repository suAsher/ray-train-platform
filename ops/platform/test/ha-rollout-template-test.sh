#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
VALUES="${ROOT}/helm/ray-train-platform/values.yaml"
PROFILE="${ROOT}/deploy/profiles/vke-cpu-ha.yaml"
CHART="${ROOT}/helm/ray-train-platform"
HELPERS="${CHART}/templates/_helpers.tpl"

grep -F 'rollingUpdate:' "$VALUES" >/dev/null
grep -F 'maxUnavailable: 0' "$VALUES" >/dev/null
grep -F 'maxSurge: 1' "$VALUES" >/dev/null
grep -F '.Values.availability.rollingUpdate.maxUnavailable' "${ROOT}/helm/ray-train-platform/templates/backend-deployment.yaml" >/dev/null
grep -F '.Values.availability.rollingUpdate.maxSurge' "${ROOT}/helm/ray-train-platform/templates/backend-deployment.yaml" >/dev/null
grep -F '.Values.availability.rollingUpdate.maxUnavailable' "${ROOT}/helm/ray-train-platform/templates/frontend-deployment.yaml" >/dev/null
grep -F '.Values.availability.rollingUpdate.maxSurge' "${ROOT}/helm/ray-train-platform/templates/frontend-deployment.yaml" >/dev/null

awk '
  /^availability:/ { in_availability=1; next }
  in_availability && /^  rollingUpdate:/ { in_rolling=1; next }
  in_rolling && /^    maxUnavailable: 1$/ { has_unavailable=1 }
  in_rolling && /^    maxSurge: 0$/ { has_surge=1 }
  END { exit !(has_unavailable && has_surge) }
' "$PROFILE"

grep -F 'preferredNodeSelector:' "$VALUES" >/dev/null
grep -F 'allowGPUNodeFallback: false' "$VALUES" >/dev/null
grep -F 'define "ray-train-platform.nodeAffinity"' "$HELPERS" >/dev/null
for template in backend-deployment.yaml frontend-deployment.yaml spk-rayjob-release.yaml postgres.yaml; do
  grep -F 'include "ray-train-platform.nodeAffinity"' "${CHART}/templates/${template}" >/dev/null
done

command -v helm >/dev/null || { echo 'missing command: helm' >&2; exit 1; }
rendered="$(mktemp)"
portable="$(mktemp)"
trap 'rm -f "$rendered" "$portable"' EXIT
helm template ray-platform "$CHART" --namespace ray-train-platform --values "$PROFILE" >"$rendered"
helm template ray-platform "$CHART" --namespace ray-train-platform \
  --values "${ROOT}/deploy/profiles/test.yaml" \
  --set training.localCache.available=false \
  --set training.localCache.storageClass= \
  --set training.localCache.mountPath= >"$portable"

if grep -A8 '^      nodeSelector:' "$rendered" | grep -Fq 'platform.wellspiking.ai/pool: control-plane'; then
  echo 'production control-plane Pods must not use a hard control-plane nodeSelector' >&2
  exit 1
fi
[[ "$(grep -c 'weight: 100' "$rendered")" -ge 4 ]] || {
  echo 'all platform workloads must render a weight-100 CPU node preference' >&2
  exit 1
}
grep -F 'key: platform.wellspiking.ai/pool' "$rendered" >/dev/null
grep -F 'values:' "$rendered" | grep -F 'control-plane' >/dev/null
grep -F 'values:' "$rendered" | grep -F 'virtual-node' >/dev/null
grep -F 'name: TRAINING_NODE_SELECTOR' "$rendered" >/dev/null
grep -F 'value: "accelerator=nvidia-rtx-4090,platform.wellspiking.ai/gpu-pool=production"' "$rendered" >/dev/null
grep -F 'name: LOCAL_CACHE_ENABLED' "$rendered" >/dev/null
grep -A1 'name: LOCAL_CACHE_ENABLED' "$rendered" | grep -F 'value: "true"' >/dev/null
grep -A1 'name: LOCAL_CACHE_SIZE' "$rendered" | grep -F 'value: "200Gi"' >/dev/null
grep -A1 'name: LOCAL_CACHE_ALLOWED_SIZES' "$rendered" | grep -F 'value: "100Gi,200Gi,500Gi"' >/dev/null
grep -A1 'name: LOCAL_CACHE_MAX_SIZE' "$rendered" | grep -F 'value: "500Gi"' >/dev/null
if ! awk '
  function verify_document() {
    if ((kind == "Deployment" || kind == "StatefulSet") && has_gpu) exit 1
  }
  $0 == "---" { verify_document(); kind = ""; has_gpu = 0; next }
  /^kind: / { kind = substr($0, 7) }
  /nvidia.com\/gpu/ { has_gpu = 1 }
  END { verify_document() }
' "$rendered"; then
  echo 'platform control-plane Pods must consume zero GPU resources' >&2
  exit 1
fi
grep -A1 'name: LOCAL_CACHE_ENABLED' "$portable" | grep -F 'value: "false"' >/dev/null

echo "HA rollout template contract passed"
