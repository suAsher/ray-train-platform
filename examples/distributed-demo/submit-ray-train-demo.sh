#!/usr/bin/env bash
set -euo pipefail

# Usage: submit-ray-train-demo.sh <image@sha256:...> <dns-safe-job-name> [gpus-per-node]
image="${1:?pinned image reference is required}"
job_name="${2:?DNS-safe job name is required}"
gpus_per_node="${3:-1}"
source_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
spk_rayjob_bin="${SPK_RAYJOB_BIN:-spk-rayjob}"

[[ "$image" == *@sha256:* ]] || { echo 'image must use a sha256 digest' >&2; exit 2; }
[[ "$gpus_per_node" =~ ^(1|2|4|8)$ ]] || { echo 'GPUs per node must be 1, 2, 4, or 8' >&2; exit 2; }

exec "$spk_rayjob_bin" submit --dir "$source_dir" --name "$job_name" --image "$image" \
  --entrypoint 'python ray_train_smoke.py' --execution-mode ray_train \
  --workers 2 --gpus-per-worker "$gpus_per_node" --cpu-per-worker 32 --memory-per-worker 128Gi \
  --output-path "validation/${job_name}"
