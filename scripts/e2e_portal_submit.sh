#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/e2e_portal_submit.sh --image <image@sha256:...> [options]

Exercise the exact Portal contract: upload code to My workspace, create an
immutable workspace snapshot, POST the same JobSpec as the browser, then wait
for the governed platform job to finish.

Options:
  --source-dir <path>       Directory containing the smoke source files.
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
[[ -f "$config" ]] || { echo "login config does not exist: $config" >&2; exit 2; }
[[ "$timeout_seconds" =~ ^[1-9][0-9]*$ ]] || { echo '--timeout must be a positive integer' >&2; exit 2; }
for file in storage_contract.py storage_gpu_smoke.py; do
  [[ -f "$source_dir/$file" ]] || { echo "missing source file: $source_dir/$file" >&2; exit 2; }
done
command -v curl >/dev/null
command -v jq >/dev/null
command -v spk-rayjob >/dev/null

token="$(jq -r '.token // empty' "$config")"
[[ -n "$token" ]] || { echo 'login config has no token' >&2; exit 2; }
curl_config="$(mktemp /tmp/ray-portal-curl.XXXXXX)"
trap 'rm -f "$curl_config"' EXIT
chmod 600 "$curl_config"

write_curl_config() {
  local target="$1"
  local with_authorization="${2:-false}"
  [[ "$target" != *$'\n'* && "$target" != *$'\r'* ]] || {
    echo 'refusing an unsafe request URL' >&2
    return 1
  }
  target="${target//\\/\\\\}"
  target="${target//\"/\\\"}"
  {
    printf 'silent\nshow-error\nfail\nurl = "%s"\n' "$target"
    if [[ "$with_authorization" == true ]]; then
      printf 'header = "Authorization: Bearer %s"\n' "$token"
    fi
  } >"$curl_config"
}

name="accept-portal-storage-$(date +%s)"
workspace_path="acceptance/${name}"

for file in storage_contract.py storage_gpu_smoke.py; do
  local_file="$source_dir/$file"
  size="$(wc -c <"$local_file" | tr -d ' ')"
  request="$(jq -cn --arg path "$workspace_path/$file" --argjson size "$size" \
    '{path:$path,contentType:"text/x-python",sizeBytes:$size}')"
  write_curl_config "${api_url%/}/api/v1/data-spaces/workspace/uploads" true
  upload="$(curl --config "$curl_config" -X POST -H 'Content-Type: application/json' --data "$request")"
  upload_url="$(jq -r '.data.url // empty' <<<"$upload")"
  content_type="$(jq -r '.data.requiredHeaders["Content-Type"] // "text/x-python"' <<<"$upload")"
  [[ -n "$upload_url" ]] || { echo "platform did not return an upload URL for $file" >&2; exit 1; }
  write_curl_config "$upload_url"
  curl --config "$curl_config" -X PUT -H "Content-Type: $content_type" -H "Content-Length: $size" \
    --data-binary "@$local_file" >/dev/null
  printf 'UPLOADED=%s/%s BYTES=%s\n' "$workspace_path" "$file" "$size"
done

snapshot_request="$(jq -cn --arg sourcePath "$workspace_path" '{sourcePath:$sourcePath}')"
write_curl_config "${api_url%/}/api/v1/workspace-snapshots" true
snapshot_response="$(curl --config "$curl_config" -X POST -H 'Content-Type: application/json' --data "$snapshot_request")"
snapshot_id="$(jq -r '.data.id // empty' <<<"$snapshot_response")"
[[ -n "$snapshot_id" ]] || { echo 'workspace snapshot was not created' >&2; exit 1; }
printf 'SNAPSHOT_ID=%s\n' "$snapshot_id"

job_request="$(jq -cn \
  --arg name "$name" --arg image "$image" --arg snapshot "$snapshot_id" \
  --arg inputPath "$dataset_path" --arg outputPath "acceptance/$name" \
  '{spec:{name:$name,image:$image,source:{type:"workspace",snapshot:$snapshot},
    entrypoint:{command:["python","storage_gpu_smoke.py"]},
    execution:{mode:"single_gpu"},
    resources:{workerReplicas:1,gpusPerWorker:1,cpuPerWorker:8,memoryPerWorker:"32Gi"},
    queue:"",input:{space:"public",relativePath:$inputPath},checkpoint:{},
    output:{space:"my-runs",relativePath:$outputPath},timeoutSeconds:600,
    retryPolicy:{maxRetries:0},cleanupPolicy:{successTtlSeconds:0,failureTtlSeconds:0}}}')"
write_curl_config "${api_url%/}/api/v1/jobs" true
job_response="$(curl --config "$curl_config" -X POST -H 'Content-Type: application/json' \
  -H "Idempotency-Key: portal-acceptance-$name" --data "$job_request")"
job_id="$(jq -r '.data.id // empty' <<<"$job_response")"
[[ -n "$job_id" ]] || { echo 'Portal job was not accepted' >&2; exit 1; }
printf 'FLOW=portal-api JOB_ID=%s NAME=%s\n' "$job_id" "$name"

deadline=$(( $(date +%s) + timeout_seconds ))
while :; do
  detail="$(spk-rayjob status --config "$config" --server "$api_url" --output json "$job_id")"
  state="$(jq -r '.observedState // .data.observedState // empty' <<<"$detail")"
  printf 'STATE=%s\n' "$state"
  case "$state" in
    SUCCEEDED) break ;;
    FAILED|CANCELED|TIMED_OUT) spk-rayjob logs --config "$config" --server "$api_url" --limit 300 "$job_id"; exit 1 ;;
  esac
  [[ "$(date +%s)" -lt "$deadline" ]] || { echo 'timed out waiting for the Portal job' >&2; exit 1; }
  sleep 5
done

spk-rayjob logs --config "$config" --server "$api_url" --limit 300 "$job_id" | grep 'STORAGE_GPU_SMOKE_OK' | tail -1
spk-rayjob status --config "$config" --server "$api_url" --output json "$job_id" \
  | jq -c '{id,submissionOrigin,observedState,createdAt,startedAt,finishedAt,input:.spec.input,output:.spec.output}'
