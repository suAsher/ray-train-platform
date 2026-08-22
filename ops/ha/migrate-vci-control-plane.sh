#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly kubectl_bin="${KUBECTL_BIN:-kubectl}"
profile=''
stage=''

usage() {
  echo 'usage: migrate-vci-control-plane.sh --profile <profile.yaml> --stage <platform|loki|controllers>' >&2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --profile) profile="${2:-}"; shift 2 ;;
    --stage) stage="${2:-}"; shift 2 ;;
    *) usage; exit 2 ;;
  esac
done

[[ -n "$profile" && -f "$profile" ]] || { usage; exit 2; }
[[ "$stage" == platform || "$stage" == loki || "$stage" == controllers ]] || { usage; exit 2; }

patch_deployment_to_cpu() {
  local namespace="$1"
  local name="$2"
  local replicas="$3"
  "$kubectl_bin" -n "$namespace" get deployment "$name" >/dev/null 2>&1 || return 0
  "$kubectl_bin" -n "$namespace" patch deployment "$name" --type merge \
    --patch '{"spec":{"replicas":'"$replicas"',"template":{"metadata":{"annotations":{"vke.volcengine.com/burst-to-vci":null}},"spec":{"affinity":{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":null}},"nodeSelector":{"node.kubernetes.io/instance-type":null,"vci.vke.volcengine.com/node-type":null,"platform.wellspiking.ai/pool":"control-plane"}}}}}' >/dev/null
  "$kubectl_bin" -n "$namespace" rollout status "deployment/${name}" --timeout=10m
}

patch_statefulset_to_cpu() {
  local namespace="$1"
  local name="$2"
  "$kubectl_bin" -n "$namespace" get statefulset "$name" >/dev/null 2>&1 || return 0
  "$kubectl_bin" -n "$namespace" patch statefulset "$name" --type merge \
    --patch '{"spec":{"template":{"metadata":{"annotations":{"vke.volcengine.com/burst-to-vci":null}},"spec":{"affinity":{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":null}},"nodeSelector":{"node.kubernetes.io/instance-type":null,"vci.vke.volcengine.com/node-type":null,"platform.wellspiking.ai/pool":"control-plane"}}}}}' >/dev/null
  "$kubectl_bin" -n "$namespace" rollout status "statefulset/${name}" --timeout=10m
}

case "$stage" in
  platform)
    bash "${root_dir}/ops/ha/label-control-plane-nodes.sh"
    bash "${root_dir}/ops/platform/deploy.sh" --profile "$profile" --verify-fsx-irsa --timeout 12m
    ;;
  loki)
    bash "${root_dir}/ops/ha/label-control-plane-nodes.sh"
    bash "${root_dir}/ops/observability/loki/migrate-vci-to-cpu.sh" --execute
    bash "${root_dir}/ops/observability/alloy/deploy-ha.sh" --install
    ;;
  controllers)
    bash "${root_dir}/ops/ha/label-control-plane-nodes.sh"
    while IFS= read -r node; do
      [[ -n "$node" ]] || continue
      "$kubectl_bin" cordon "$node"
    done < <("$kubectl_bin" get nodes -l node.kubernetes.io/instance-type=virtual-node -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')

    patch_deployment_to_cpu kuberay-system kuberay-operator 2
    patch_deployment_to_cpu kueue-system kueue-controller-manager 2
    patch_deployment_to_cpu kube-system apig-controller 2
    patch_deployment_to_cpu kube-system coredns 2
    patch_deployment_to_cpu kube-system csi-ebs-controller 1
    patch_deployment_to_cpu kube-system csi-nas-controller 1
    patch_deployment_to_cpu kube-system csi-fsx-controller 1
    patch_deployment_to_cpu kube-system fsx-agent-default 1
    patch_deployment_to_cpu kube-system metrics-server 1
    patch_deployment_to_cpu kube-system snapshot-controller 1
    patch_statefulset_to_cpu kube-system csi-cloudfs-controller
    ;;
esac
