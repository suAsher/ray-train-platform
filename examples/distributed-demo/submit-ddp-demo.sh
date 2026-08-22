#!/usr/bin/env bash
set -euo pipefail

# Usage: submit-ddp-demo.sh <image@sha256:...> <dns-safe-job-name> [gpu-count]
image="${1:?pinned image reference is required}"
job_name="${2:?DNS-safe job name is required}"
gpu_count="${3:-2}"
source_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
spk_rayjob_bin="${SPK_RAYJOB_BIN:-spk-rayjob}"

[[ "$image" == *@sha256:* ]] || { echo 'image must use a sha256 digest' >&2; exit 2; }
[[ "$gpu_count" =~ ^(2|4|8)$ ]] || { echo 'gpu count must be 2, 4, or 8' >&2; exit 2; }

exec "$spk_rayjob_bin" submit --dir "$source_dir" --name "$job_name" --image "$image" \
  --entrypoint 'python ddp_smoke.py' --execution-mode torchrun \
  --workers 1 --gpus-per-worker "$gpu_count" --cpu-per-worker 32 --memory-per-worker 128Gi \
  --output-path "validation/${job_name}"
