#!/usr/bin/env bash
set -euo pipefail

# This is intentionally explicit rather than discovering every NVIDIA node:
# the legacy single-GPU smoke node must never become part of a 16-GPU job by
# accident. Update the array as part of the documented node-addition process.
readonly kubectl_bin="${KUBECTL_BIN:-kubectl}"
readonly production_nodes=(172.28.1.232 172.28.1.233)
readonly legacy_smoke_nodes=(172.28.0.167)
readonly accelerator_label='accelerator=nvidia-rtx-4090'
readonly production_pool_label='platform.wellspiking.ai/gpu-pool=production'
readonly legacy_pool_label='platform.wellspiking.ai/gpu-pool=legacy-test'

require_node_gpu_count() {
  local node="$1"
  local expected="$2"
  local accelerator gpus
  "$kubectl_bin" get node "$node" >/dev/null
  accelerator="$("$kubectl_bin" get node "$node" -o jsonpath='{.metadata.labels.accelerator}')"
  gpus="$("$kubectl_bin" get node "$node" -o go-template='{{ index .status.allocatable "nvidia.com/gpu" }}')"
  [[ "$accelerator" == 'nvidia-rtx-4090' ]] || { echo "refusing ${node}: accelerator=${accelerator:-unset}" >&2; exit 1; }
  [[ "$gpus" == "$expected" ]] || { echo "refusing ${node}: allocatable GPUs=${gpus:-unset}, expected=${expected}" >&2; exit 1; }
}

for node in "${production_nodes[@]}"; do
  require_node_gpu_count "$node" 8
  "$kubectl_bin" label node "$node" "$production_pool_label" --overwrite
done

for node in "${legacy_smoke_nodes[@]}"; do
  require_node_gpu_count "$node" 1
  "$kubectl_bin" label node "$node" "$legacy_pool_label" --overwrite
done

echo "production GPU pool labelled: ${production_nodes[*]} (16 GPUs); legacy smoke pool labelled: ${legacy_smoke_nodes[*]}"
