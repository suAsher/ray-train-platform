#!/usr/bin/env bash
set -euo pipefail

readonly kubectl_bin="${KUBECTL_BIN:-kubectl}"
readonly cpu_pool_label='platform.wellspiking.ai/pool'
readonly cpu_pool_value='control-plane'
readonly virtual_selector='node.kubernetes.io/instance-type=virtual-node'

for required in "$kubectl_bin" jq; do
  command -v "$required" >/dev/null || { echo "missing required command: ${required}" >&2; exit 1; }
done

failures=0

fail() {
  echo "VCI retirement check failed: $*" >&2
  failures=1
}

require_ready() {
  local kind="$1"
  local namespace="$2"
  local name="$3"
  local expected="$4"
  local ready
  ready="$("$kubectl_bin" -n "$namespace" get "$kind" "$name" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || true)"
  ready="${ready:-0}"
  [[ "$ready" =~ ^[0-9]+$ && "$ready" -ge "$expected" ]] || fail "${namespace}/${kind}/${name} ready=${ready}, expected>=${expected}"
}

require_cpu_pool() {
  local namespace="$1"
  local selector="$2"
  local expected="$3"
  local pods nodes node pool count
  pods="$("$kubectl_bin" -n "$namespace" get pods -l "$selector" -o json)"
  count="$(jq '[.items[] | select(.status.phase == "Running") | select(any(.status.containerStatuses[]?; .ready == true))] | length' <<<"$pods")"
  [[ "$count" =~ ^[0-9]+$ && "$count" -ge "$expected" ]] || { fail "${namespace}/${selector} readyPods=${count}, expected>=${expected}"; return; }
  nodes="$(jq -r '.items[] | select(.status.phase == "Running") | .spec.nodeName' <<<"$pods" | sort -u)"
  while IFS= read -r node; do
    [[ -n "$node" ]] || continue
    pool="$("$kubectl_bin" get node "$node" -o go-template="{{ index .metadata.labels \"${cpu_pool_label}\" }}")"
    [[ "$pool" == "$cpu_pool_value" ]] || fail "${namespace}/${selector} uses node outside CPU pool: ${node}"
  done <<<"$nodes"
}

require_ready deployment ray-train-platform ray-train-backend 2
require_ready deployment ray-train-platform ray-train-frontend 2
require_ready statefulset ray-train-platform postgres 1
require_ready statefulset loki loki-cpu 3
require_ready deployment loki loki-cpu-gateway 2
require_ready statefulset monitoring alloy 2
require_ready deployment kuberay-system kuberay-operator 2
require_ready deployment kueue-system kueue-controller-manager 2
require_ready deployment kube-system coredns 2

require_cpu_pool ray-train-platform 'app=ray-train-backend' 2
require_cpu_pool ray-train-platform 'app=ray-train-frontend' 2
require_cpu_pool ray-train-platform 'app=ray-train-postgres' 1
require_cpu_pool loki 'app.kubernetes.io/instance=loki-cpu' 3
require_cpu_pool monitoring 'app.kubernetes.io/instance=alloy' 2
require_cpu_pool kuberay-system 'app.kubernetes.io/name=kuberay-operator' 2
require_cpu_pool kueue-system 'app.kubernetes.io/name=kueue' 2
require_cpu_pool kube-system 'k8s-app=kube-dns' 2

virtual_nodes="$("$kubectl_bin" get nodes -l "$virtual_selector" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')"
while IFS= read -r node; do
  [[ -n "$node" ]] || continue
  active_pods="$("$kubectl_bin" get pods -A --field-selector "spec.nodeName=${node}" -o json | jq -r '.items[] | select(.status.phase == "Running" or .status.phase == "Pending") | select((.metadata.ownerReferences[0].kind // "") != "DaemonSet") | "\(.metadata.namespace)/\(.metadata.name)"')"
  if [[ -n "$active_pods" ]]; then
    fail "virtual node ${node} still has non-DaemonSet workloads: $(tr '\n' ' ' <<<"$active_pods")"
  fi
done <<<"$virtual_nodes"

[[ "$failures" == 0 ]] || exit 1
echo 'VCI retirement readiness verified: all critical workloads run on the CPU pool'
