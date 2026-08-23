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
workload_document="$(mktemp)"
trap 'rm -f "$rendered" "$portable" "$workload_document"' EXIT
helm template ray-platform "$CHART" --namespace ray-train-platform --values "$PROFILE" >"$rendered"
helm template ray-platform "$CHART" --namespace ray-train-platform \
  --values "${ROOT}/deploy/profiles/test.yaml" \
  --set training.localCache.available=false \
  --set training.localCache.storageClass= \
  --set training.localCache.mountPath= >"$portable"

extract_workload_document() {
  local source="$1"
  local wanted_kind="$2"
  local wanted_name="$3"
  local destination="$4"

  awk -v wanted_kind="$wanted_kind" -v wanted_name="$wanted_name" '
    function emit_if_wanted() {
      if (document_kind == wanted_kind && document_name == wanted_name) {
        printf "%s", document
        found = 1
      }
    }
    $0 == "---" {
      emit_if_wanted()
      document = ""
      document_kind = ""
      document_name = ""
      in_metadata = 0
      next
    }
    {
      document = document $0 ORS
      if ($0 ~ /^kind: /) document_kind = substr($0, 7)
      if ($0 == "metadata:") {
        in_metadata = 1
      } else if (in_metadata && $0 ~ /^  name: /) {
        document_name = substr($0, 9)
        in_metadata = 0
      } else if (in_metadata && $0 !~ /^  /) {
        in_metadata = 0
      }
    }
    END {
      emit_if_wanted()
      exit found ? 0 : 1
    }
  ' "$source" >"$destination"
}

assert_platform_placement() {
  local workload="$1"

  if ! awk -v workload="$workload" '
    function indentation(line) {
      return match(line, /[^ ]/) ? RSTART - 1 : 999
    }
    /^[[:space:]]+nodeSelector:$/ {
      in_node_selector = 1
      selector_indent = indentation($0)
      next
    }
    {
      current_indent = indentation($0)
      if ($0 != "" && current_indent <= selector_indent) in_node_selector = 0
      if (in_node_selector && /platform\.wellspiking\.ai\/pool:/ && /control-plane/) hard_control_plane = 1

      if ($0 ~ /^[[:space:]]+nodeAffinity:$/) {
        in_node_affinity = 1
        node_affinity_indent = current_indent
        next
      }
      if (in_node_affinity && $0 != "" && current_indent <= node_affinity_indent) {
        in_node_affinity = 0
        affinity_section = ""
      }
      if (!in_node_affinity) next

      if ($0 ~ /requiredDuringSchedulingIgnoredDuringExecution:$/) {
        affinity_section = "required"
        has_required = 1
        next
      }
      if ($0 ~ /preferredDuringSchedulingIgnoredDuringExecution:$/) {
        affinity_section = "preferred"
        has_preferred = 1
        next
      }

      if (affinity_section == "required" && $0 ~ /- key: node\.kubernetes\.io\/instance-type$/) {
        required_expression = "instance-type"
        required_instance_key = 1
      } else if (affinity_section == "required" && $0 ~ /- key: type$/) {
        required_expression = "type"
        required_type_key = 1
      } else if (affinity_section == "required" && $0 ~ /- key:/) {
        required_expression = ""
      }
      if (required_expression == "instance-type" && $0 ~ /operator: NotIn$/) required_instance_not_in = 1
      if (required_expression == "instance-type" && $0 ~ /values: \["virtual-node"\]$/) required_virtual_node = 1
      if (required_expression == "type" && $0 ~ /operator: NotIn$/) required_type_not_in = 1
      if (required_expression == "type" && $0 ~ /values: \["virtual-kubelet"\]$/) required_virtual_kubelet = 1

      if (affinity_section == "preferred" && $0 ~ /- weight: 100$/) preferred_weight = 1
      if (affinity_section == "preferred" && $0 ~ /- key: platform\.wellspiking\.ai\/pool$/) {
        preferred_expression = 1
        preferred_key = 1
      } else if (affinity_section == "preferred" && $0 ~ /- key:/) {
        preferred_expression = 0
      }
      if (preferred_expression && $0 ~ /operator: In$/) preferred_operator = 1
      if (preferred_expression && $0 ~ /values: \["control-plane"\]$/) preferred_value = 1
    }
    END {
      failed = 0
      if (hard_control_plane) {
        print workload " must not hard-select control-plane nodes" > "/dev/stderr"
        failed = 1
      }
      if (!(has_preferred && preferred_weight && preferred_key && preferred_operator && preferred_value)) {
        print workload " must prefer control-plane nodes with node-affinity weight 100" > "/dev/stderr"
        failed = 1
      }
      if (!(has_required && required_instance_key && required_instance_not_in && required_virtual_node && required_type_key && required_type_not_in && required_virtual_kubelet)) {
        print workload " must retain required virtual-node exclusions" > "/dev/stderr"
        failed = 1
      }
      exit failed
    }
  ' "$workload_document"; then
    exit 1
  fi

  if grep -Fq 'nvidia.com/gpu' "$workload_document"; then
    echo "${workload} must consume zero GPU resources" >&2
    exit 1
  fi
}

for workload_spec in \
  'Deployment:ray-train-backend' \
  'Deployment:ray-train-frontend' \
  'Deployment:spk-rayjob-release' \
  'StatefulSet:postgres'; do
  workload_kind="${workload_spec%%:*}"
  workload_name="${workload_spec#*:}"
  extract_workload_document "$rendered" "$workload_kind" "$workload_name" "$workload_document" || {
    echo "VKE render missing ${workload_kind} ${workload_name}" >&2
    exit 1
  }
  assert_platform_placement "$workload_name"

  if [[ "$workload_name" == 'ray-train-backend' ]]; then
    grep -A1 'name: TRAINING_NODE_SELECTOR' "$workload_document" \
      | grep -F 'value: "accelerator=nvidia-rtx-4090,platform.wellspiking.ai/gpu-pool=production"' >/dev/null
    grep -A1 'name: LOCAL_CACHE_ENABLED' "$workload_document" | grep -F 'value: "true"' >/dev/null
    grep -A1 'name: LOCAL_CACHE_SIZE' "$workload_document" | grep -F 'value: "200Gi"' >/dev/null
    grep -A1 'name: LOCAL_CACHE_ALLOWED_SIZES' "$workload_document" | grep -F 'value: "100Gi,200Gi,500Gi"' >/dev/null
    grep -A1 'name: LOCAL_CACHE_MAX_SIZE' "$workload_document" | grep -F 'value: "500Gi"' >/dev/null
  fi
done

extract_workload_document "$portable" Deployment ray-train-backend "$workload_document" || {
  echo 'portable render missing Deployment ray-train-backend' >&2
  exit 1
}
grep -A1 'name: LOCAL_CACHE_ENABLED' "$workload_document" | grep -F 'value: "false"' >/dev/null

echo "HA rollout template contract passed"
