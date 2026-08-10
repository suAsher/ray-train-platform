#!/usr/bin/env bash
set -Eeuo pipefail

API_URL="${API_URL:?set API_URL, for example https://ai-training.internal.example}"
ACCESS_TOKEN="${ACCESS_TOKEN:-}"
ALLOW_ANONYMOUS="${ALLOW_ANONYMOUS:-false}"
IMAGE="${IMAGE:?set an immutable image reference with @sha256:<64 hex chars>}"
GIT_URL="${GIT_URL:?set a Git repository URL allowed by the platform}"
GIT_COMMIT="${GIT_COMMIT:?set an immutable Git commit SHA}"
ENTRYPOINT="${ENTRYPOINT:-python train.py}"
WORKER_REPLICAS="${WORKER_REPLICAS:-1}"
GPUS_PER_WORKER="${GPUS_PER_WORKER:-1}"
JOB_NAME="${JOB_NAME:-e2e-ray-training-$(date +%s)}"

if [[ -z "$ACCESS_TOKEN" && "$ALLOW_ANONYMOUS" != "true" ]]; then
  printf 'set ACCESS_TOKEN, or explicitly set ALLOW_ANONYMOUS=true for the isolated demo profile\n' >&2
  exit 1
fi
AUTH_ARGS=()
if [[ -n "$ACCESS_TOKEN" ]]; then
  AUTH_ARGS=(-H "Authorization: Bearer $ACCESS_TOKEN")
fi

need() { command -v "$1" >/dev/null 2>&1 || { printf 'missing command: %s\n' "$1" >&2; exit 1; }; }
need curl
need jq

payload="$(jq -n --arg name "$JOB_NAME" --arg image "$IMAGE" --arg url "$GIT_URL" --arg commit "$GIT_COMMIT" --arg entrypoint "$ENTRYPOINT" --argjson workers "$WORKER_REPLICAS" --argjson gpus "$GPUS_PER_WORKER" '{spec:{name:$name,image:$image,source:{type:"git",url:$url,commit:$commit},entrypoint:{command:[($entrypoint|split(" "))[0]],args:(($entrypoint|split(" "))[1:])},resources:{workerReplicas:$workers,gpusPerWorker:$gpus,cpuPerWorker:8,memoryPerWorker:"32Gi"},queue:""}}')"
response="$(curl --fail-with-body --silent --show-error -X POST "$API_URL/api/v1/jobs" "${AUTH_ARGS[@]}" -H 'Content-Type: application/json' -H "Idempotency-Key: $JOB_NAME" -d "$payload")"
job_id="$(jq -r '.data.id' <<<"$response")"
[[ -n "$job_id" && "$job_id" != null ]] || { printf '%s\n' "$response" >&2; exit 1; }

for _ in $(seq 1 120); do
  detail="$(curl --fail --silent --show-error "$API_URL/api/v1/jobs/$job_id" "${AUTH_ARGS[@]}")"
  state="$(jq -r '.data.observedState' <<<"$detail")"
  printf '%s %s\n' "$job_id" "$state"
  case "$state" in
    SUCCEEDED) curl --fail --silent "$API_URL/api/v1/jobs/$job_id/logs?limit=20" "${AUTH_ARGS[@]}" >/dev/null; exit 0 ;;
    FAILED|CANCELED|TIMED_OUT) printf '%s\n' "$detail" >&2; exit 1 ;;
  esac
  sleep 5
done
printf 'training job timed out in acceptance window: %s\n' "$job_id" >&2
exit 1
