#!/usr/bin/env bash
set -euo pipefail

readonly kubectl_bin="${KUBECTL_BIN:-kubectl}"
readonly pool_label='platform.wellspiking.ai/pool=control-plane'
readonly cpu_nodes=(172.28.2.65 172.28.2.66 172.28.2.67)

for node in "${cpu_nodes[@]}"; do
  "$kubectl_bin" get node "$node" >/dev/null
  instance_type="$("$kubectl_bin" get node "$node" -o go-template='{{ index .metadata.labels "node.kubernetes.io/instance-type" }}')"
  [[ "$instance_type" != 'virtual-node' ]] || { echo "refusing to label VCI node: ${node}" >&2; exit 1; }
  accelerator="$("$kubectl_bin" get node "$node" -o jsonpath='{.metadata.labels.accelerator}')"
  [[ -z "$accelerator" ]] || { echo "refusing to label GPU node: ${node}" >&2; exit 1; }
  "$kubectl_bin" label node "$node" "$pool_label" --overwrite
done

echo "control-plane pool label applied to ${#cpu_nodes[@]} CPU nodes"
