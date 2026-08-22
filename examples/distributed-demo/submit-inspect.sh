#!/usr/bin/env bash
set -euo pipefail

# Submit a short, real GPU data-contract check.  The chosen public subdirectory
# is mounted read-only as PLATFORM_DATASET_PATH and the JSON report is written
# to this task's governed personal output directory.
#
# Usage:
#   examples/distributed-demo/submit-inspect.sh \
#     <image@sha256:...> <dns-safe-name> <public-subdirectory>

image="${1:?pinned image reference is required}"
job_name="${2:?DNS-safe job name is required}"
input_path="${3:?public data subdirectory is required}"
spk_rayjob_bin="${SPK_RAYJOB_BIN:-spk-rayjob}"
spk_rayjob_config="${SPK_RAYJOB_CONFIG:-}"
output_path="${OUTPUT_PATH:-validation/${job_name}}"
source_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ "$image" != *@sha256:* ]]; then
  echo 'image must be pinned by sha256 digest' >&2
  exit 2
fi

args=(submit)
if [[ -n "$spk_rayjob_config" ]]; then
  args+=(--config "$spk_rayjob_config")
fi
args+=(
  --dir "$source_dir"
  --name "$job_name"
  --image "$image"
  --entrypoint 'python inspect_dataset.py'
  --workers 1
  --gpus-per-worker 1
  --cpu-per-worker 8
  --memory-per-worker 32Gi
  --input-space public
  --input-path "$input_path"
  --output-path "$output_path"
)

exec "$spk_rayjob_bin" "${args[@]}"
