#!/usr/bin/env bash
set -euo pipefail

readonly kubectl_bin="${KUBECTL_BIN:-kubectl}"
readonly production_selector='platform.wellspiking.ai/gpu-pool=production'
readonly expected_nodes=2
readonly expected_gpus_per_node=8
readonly expected_total_gpus=16

for required in "$kubectl_bin" jq; do
  command -v "$required" >/dev/null || { echo "missing required command: ${required}" >&2; exit 1; }
done

nodes_json="$("$kubectl_bin" get nodes -l "$production_selector" -o json)"
count="$(jq '.items | length' <<<"$nodes_json")"
[[ "$count" == "$expected_nodes" ]] || { echo "production pool has ${count} nodes, expected ${expected_nodes}" >&2; exit 1; }

total_gpus=0
while IFS=$'\t' read -r name ready accelerator gpus; do
  [[ "$ready" == 'True' ]] || { echo "production node ${name} is not Ready" >&2; exit 1; }
  [[ "$accelerator" == 'nvidia-rtx-4090' ]] || { echo "production node ${name} has unexpected accelerator=${accelerator:-unset}" >&2; exit 1; }
  [[ "$gpus" == "$expected_gpus_per_node" ]] || { echo "production node ${name} has ${gpus:-unset} GPUs, expected ${expected_gpus_per_node}" >&2; exit 1; }
  total_gpus=$((total_gpus + gpus))
done < <(jq -r '.items[] | [.metadata.name, ([.status.conditions[] | select(.type == "Ready") | .status][0] // "False"), (.metadata.labels.accelerator // ""), (.status.allocatable["nvidia.com/gpu"] // "0")] | @tsv' <<<"$nodes_json")

[[ "$total_gpus" == "$expected_total_gpus" ]] || { echo "production pool has ${total_gpus} GPUs, expected ${expected_total_gpus}" >&2; exit 1; }

queue_gpus="$("$kubectl_bin" get clusterqueue cluster-gpu-queue -o json | jq -r '[.spec.resourceGroups[].flavors[].resources[] | select(.name == "nvidia.com/gpu") | .nominalQuota] | first // ""')"
[[ "$queue_gpus" == "$expected_total_gpus" ]] || { echo "cluster-gpu-queue GPU quota=${queue_gpus:-unset}, expected ${expected_total_gpus}" >&2; exit 1; }

echo "production GPU pool verified: ${count} Ready nodes, ${total_gpus} GPUs, Kueue quota ${queue_gpus}"
