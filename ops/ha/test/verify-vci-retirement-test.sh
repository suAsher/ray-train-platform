#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly verify_script="${root_dir}/ops/ha/verify-vci-retirement.sh"
readonly migrate_script="${root_dir}/ops/ha/migrate-vci-control-plane.sh"
readonly label_script="${root_dir}/ops/ha/label-control-plane-nodes.sh"

for required in "$verify_script" "$migrate_script" "$label_script"; do
  [[ -f "$required" ]] || { echo "missing VCI retirement tool: ${required}" >&2; exit 1; }
done

grep -Fq 'node.kubernetes.io/instance-type=virtual-node' "$verify_script"
grep -Fq 'platform.wellspiking.ai/pool' "$verify_script"
grep -Fq 'ray-train-backend' "$verify_script"
grep -Fq 'loki-cpu' "$verify_script"
grep -Fq 'cordon "$node"' "$migrate_script"
grep -Fq 'rollout status' "$migrate_script"
grep -Fq '"node.kubernetes.io/instance-type":null' "$migrate_script"
grep -Fq '"vci.vke.volcengine.com/node-type":null' "$migrate_script"
grep -Fq '"vke.volcengine.com/burst-to-vci":null' "$migrate_script"
grep -Fq '"requiredDuringSchedulingIgnoredDuringExecution":null' "$migrate_script"
grep -Fq '172.28.2.65' "$label_script"
grep -Fq '172.28.2.66' "$label_script"
grep -Fq '172.28.2.67' "$label_script"
grep -Fq "jsonpath='{.metadata.labels.accelerator}'" "$label_script"

for forbidden in ' delete ' ' delete-' ' drain ' ' rayjob ' ' raycluster '; do
  if grep -Fq -- "$forbidden" "$migrate_script"; then
    echo "unsafe VCI migration command contains:${forbidden}" >&2
    exit 1
  fi
done

echo 'VCI retirement operation contract verified'
