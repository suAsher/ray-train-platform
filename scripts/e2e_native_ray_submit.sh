#!/usr/bin/env bash
set -euo pipefail

# This image is controlled by the platform and contains the stock Ray 2.35
# client. The user's workload image is passed as validated platform metadata;
# never run an arbitrary workload image with the caller's PAT in its env.
readonly RAY_CLI_IMAGE="harbor.wellspiking.ai/guofeng.su/ray-train-pytorch@sha256:db39733e3e5d301547286dd8f532dd2c78c16c4e0806e3e2096189d0696e5288"

usage() {
  cat <<'EOF'
Usage: scripts/e2e_native_ray_submit.sh --image <image@sha256:...> [options]

Submit the current distributed-demo directory through the stock Ray 2.35 CLI,
then find and follow the corresponding governed platform job.

Options:
  --source-dir <path>       Working directory uploaded by Ray.
  --config <path>           Existing spk-rayjob login config.
  --api-url <url>           Platform origin (default: https://raytrain.wellspiking.ai).
  --dataset-path <path>     Path below the public data space.
  --timeout <seconds>       Completion timeout (default: 900).
EOF
}

image=""
source_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../examples/distributed-demo" && pwd)"
config="${HOME}/.config/spk-rayjob/config.json"
api_url="https://raytrain.wellspiking.ai"
dataset_path="bevfusion/fz-3dod-v1/platform-validation/annotations/fz-0429-platform-smoke-128"
timeout_seconds=900

while (($#)); do
  case "$1" in
    --image) image="${2:-}"; shift 2 ;;
    --source-dir) source_dir="${2:-}"; shift 2 ;;
    --config) config="${2:-}"; shift 2 ;;
    --api-url) api_url="${2:-}"; shift 2 ;;
    --dataset-path) dataset_path="${2:-}"; shift 2 ;;
    --timeout) timeout_seconds="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ "$image" == *@sha256:* ]] || { echo '--image must use an immutable sha256 digest' >&2; exit 2; }
[[ "$api_url" =~ ^https://[^/]+/?$ ]] || { echo '--api-url must be an HTTPS origin' >&2; exit 2; }
[[ -d "$source_dir" ]] || { echo "source directory does not exist: $source_dir" >&2; exit 2; }
[[ -f "$config" ]] || { echo "login config does not exist: $config" >&2; exit 2; }
[[ "$timeout_seconds" =~ ^[1-9][0-9]*$ ]] || { echo '--timeout must be a positive integer' >&2; exit 2; }
command -v docker >/dev/null
command -v jq >/dev/null
command -v spk-rayjob >/dev/null

submission_id="native-storage-$(date +%s)"
output_path="/mnt/storage/me/runs/acceptance/${submission_id}"
metadata="$(jq -cn --arg image "$image" '{
  "ray-platform.image":$image,
  "ray-platform.worker-replicas":"1",
  "ray-platform.gpus-per-worker":"1",
  "ray-platform.cpu-per-worker":"8",
  "ray-platform.memory-per-worker":"32Gi",
  "ray-platform.queue":"local-gpu"
}')"
env_file="$(mktemp /tmp/ray-native-env.XXXXXX)"
trap 'rm -f "$env_file"' EXIT
chmod 600 "$env_file"
jq -r --arg address "${api_url%/}/ray" '
  ["RAY_ADDRESS=" + $address,
   "RAY_JOB_HEADERS=" + ({Authorization:("Bearer " + .token)} | tojson)] | .[]
' "$config" >"$env_file"

printf 'FLOW=native-ray SUBMISSION_ID=%s\n' "$submission_id"
docker run --rm --env-file "$env_file" \
  -v "$source_dir:/source:ro" -w /source "$RAY_CLI_IMAGE" \
  ray job submit --no-wait --submission-id "$submission_id" --working-dir . \
  --metadata-json "$metadata" -- \
  env "PLATFORM_DATASET_PATH=/mnt/storage/public/${dataset_path}" \
      "PLATFORM_OUTPUT_PATH=${output_path}" \
  python storage_gpu_smoke.py

deadline=$(( $(date +%s) + timeout_seconds ))
platform_job=""
while [[ -z "$platform_job" ]]; do
  platform_job="$(spk-rayjob jobs --config "$config" --server "$api_url" --output json \
    | jq -r --arg id "$submission_id" '.items[] | select(.externalSubmissionId == $id) | .id' \
    | head -1)"
  [[ "$(date +%s)" -lt "$deadline" ]] || { echo 'timed out waiting for the platform job record' >&2; exit 1; }
  [[ -n "$platform_job" ]] || sleep 2
done
printf 'PLATFORM_JOB_ID=%s\n' "$platform_job"

while :; do
  detail="$(spk-rayjob status --config "$config" --server "$api_url" --output json "$platform_job")"
  state="$(jq -r '.observedState // .data.observedState // empty' <<<"$detail")"
  printf 'STATE=%s\n' "$state"
  case "$state" in
    SUCCEEDED) break ;;
    FAILED|CANCELED|TIMED_OUT) spk-rayjob logs --config "$config" --server "$api_url" --limit 300 "$platform_job"; exit 1 ;;
  esac
  [[ "$(date +%s)" -lt "$deadline" ]] || { echo 'timed out waiting for the native Ray job' >&2; exit 1; }
  sleep 5
done

spk-rayjob logs --config "$config" --server "$api_url" --limit 300 "$platform_job" | grep 'STORAGE_GPU_SMOKE_OK' | tail -1
spk-rayjob status --config "$config" --server "$api_url" --output json "$platform_job" \
  | jq -c '{id,externalSubmissionId,submissionOrigin,observedState,createdAt,startedAt,finishedAt}'
