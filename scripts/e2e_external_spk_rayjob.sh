#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly FIXTURE_DIR="${ROOT_DIR}/scripts/fixtures/external-spk-rayjob-e2e"

usage() {
  cat <<'EOF'
Usage: scripts/e2e_external_spk_rayjob.sh --api-url <https://raytrain.example> \
  --image <harbor.example/project/image@sha256:...> [options]

Runs the full external-user route without printing any credential:
platform-account login -> source package -> governed TOS upload -> RayJob ->
one GPU task -> output write -> status and logs.

Options:
  --resolve <private-alb-ip>   Validate a private ALB before DNS cutover.
  --namespace <name>           Platform namespace (default: ray-train-platform).
  --username <name>            Existing local Engineer account (default: admin).
  --bootstrap-secret <name>    Secret holding BOOTSTRAP_ADMIN_PASSWORD.
  --timeout <seconds>          Completion timeout (default: 1200).
EOF
}

api_url=""
training_image=""
resolve_ip=""
namespace="ray-train-platform"
username="admin"
bootstrap_secret="ray-platform-bootstrap-admin"
timeout_seconds=1200

while (($#)); do
  case "$1" in
    --api-url) api_url="${2:-}"; shift 2 ;;
    --image) training_image="${2:-}"; shift 2 ;;
    --resolve) resolve_ip="${2:-}"; shift 2 ;;
    --namespace) namespace="${2:-}"; shift 2 ;;
    --username) username="${2:-}"; shift 2 ;;
    --bootstrap-secret) bootstrap_secret="${2:-}"; shift 2 ;;
    --timeout) timeout_seconds="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ "$api_url" =~ ^https://[^/]+/?$ ]] || { echo '--api-url must be an HTTPS origin without a path' >&2; exit 2; }
[[ "$training_image" == *@sha256:* ]] || { echo '--image must be immutable and include @sha256:' >&2; exit 2; }
[[ "$timeout_seconds" =~ ^[1-9][0-9]*$ ]] || { echo '--timeout must be a positive number of seconds' >&2; exit 2; }
[[ -d "$FIXTURE_DIR" ]] || { echo "missing fixture directory: $FIXTURE_DIR" >&2; exit 2; }
command -v kubectl >/dev/null || { echo 'kubectl is required' >&2; exit 2; }
command -v docker >/dev/null || { echo 'docker is required' >&2; exit 2; }
command -v base64 >/dev/null || { echo 'base64 is required' >&2; exit 2; }

host="${api_url#https://}"
host="${host%/}"
host="${host%%:*}"
[[ -n "$host" ]] || { echo 'could not determine API host' >&2; exit 2; }

release_image="$(kubectl -n "$namespace" get deployment spk-rayjob-release -o jsonpath='{.spec.template.spec.containers[0].image}')"
[[ "$release_image" == *@sha256:* ]] || { echo 'spk-rayjob release must use an immutable image digest' >&2; exit 1; }
kubectl -n "$namespace" get secret "$bootstrap_secret" >/dev/null

job_name="external-cli-e2e-$(date +%s)"
docker_args=(--rm -i --entrypoint /bin/sh -v "${FIXTURE_DIR}:/source:ro" -e "API_URL=${api_url}" -e "USERNAME=${username}" -e "TRAINING_IMAGE=${training_image}" -e "JOB_NAME=${job_name}" -e "TIMEOUT_SECONDS=${timeout_seconds}")
if [[ -n "$resolve_ip" ]]; then
  docker_args+=(--add-host "${host}:${resolve_ip}")
fi

printf 'Submitting external CLI acceptance job %s.\n' "$job_name"
kubectl -n "$namespace" get secret "$bootstrap_secret" -o jsonpath='{.data.BOOTSTRAP_ADMIN_PASSWORD}' \
  | base64 -d \
  | docker run "${docker_args[@]}" "$release_image" -ec '
      bin=/usr/share/nginx/html/downloads/spk-rayjob/spk-rayjob-linux-amd64
      config=/tmp/spk-rayjob-config.json
      "$bin" version
      "$bin" login --server "$API_URL" --username "$USERNAME" --password-stdin --config "$config"

      submit_json="$("$bin" submit --output json --config "$config" --dir /source --name "$JOB_NAME" --image "$TRAINING_IMAGE" --entrypoint "python train.py" --workers 1 --gpus-per-worker 1 --cpu-per-worker 4 --memory-per-worker 16Gi --output-path "e2e/$JOB_NAME")"
      job_id="$(printf "%s" "$submit_json" | sed -n "s/.*\"id\":\"\([^\"]*\)\".*/\1/p")"
      test -n "$job_id"
      printf "RayJob accepted: %s\n" "$job_id"

      deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
      while :; do
        detail="$("$bin" status --output json --config "$config" "$job_id")"
        case "$detail" in
          *"SUCCEEDED"*)
            "$bin" logs --output json --config "$config" --limit 200 "$job_id" | grep -F "EXTERNAL_SPK_RAYJOB_E2E"
            exit 0
            ;;
          *"FAILED"*|*"CANCELED"*|*"TIMED_OUT"*)
            printf "RayJob did not succeed: %s\n" "$job_id" >&2
            exit 1
            ;;
        esac
        if [ "$(date +%s)" -ge "$deadline" ]; then
          printf "Timed out waiting for RayJob: %s\n" "$job_id" >&2
          exit 1
        fi
        sleep 5
      done
    '

ray_cluster_name="$(kubectl -n "$namespace" get rayjob "$job_name" -o jsonpath='{.status.rayClusterName}')"
[[ -n "$ray_cluster_name" ]] || { echo "RayCluster is missing for acceptance job: $job_name" >&2; exit 1; }
head_pod="$(kubectl -n "$namespace" get pods -l "ray.io/cluster=${ray_cluster_name},ray.io/node-type=head" -o jsonpath='{.items[0].metadata.name}')"
[[ -n "$head_pod" ]] || { echo "Ray head Pod is missing for acceptance job: $job_name" >&2; exit 1; }
kubectl -n "$namespace" exec "$head_pod" -- sh -lc 'test -s "$PLATFORM_OUTPUT_PATH/external-spk-rayjob-e2e.json"'
printf 'External spk-rayjob acceptance succeeded: %s (governed output: external-spk-rayjob-e2e.json)\n' "$job_name"
