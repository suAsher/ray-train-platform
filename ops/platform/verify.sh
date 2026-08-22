#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

usage() {
  cat <<'EOF'
Usage: ops/platform/verify.sh --profile <profile.yaml> [--smoke]
       [--api-url <https://train.example>] [--smoke-image <image@sha256:...>]
       [--env-file <secret.env>]

Without --smoke this checks the installed control plane. With --smoke it also
submits a one-GPU RayJob through the public Portal API, waits for completion,
and verifies the job log endpoint. No secret is printed.
EOF
}

rendered_ingress_names() {
  local manifest="$1"
  awk '
    function flush_document() {
      if (kind == "Ingress" && name != "") print name
    }
    /^---$/ { flush_document(); kind = ""; name = ""; next }
    /^kind: / { kind = $2; next }
    kind == "Ingress" && name == "" && /^  name: / { name = $2 }
    END { flush_document() }
  ' "$manifest"
}

profile=""
run_smoke=false
api_url=""
smoke_image=""
env_file=""
while (($#)); do
  case "$1" in
    --profile) profile="${2:-}"; shift 2 ;;
    --smoke) run_smoke=true; shift ;;
    --api-url) api_url="${2:-}"; shift 2 ;;
    --smoke-image) smoke_image="${2:-}"; shift 2 ;;
    --env-file) env_file="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

require_profile "$profile"
require_command "$KUBECTL_BIN"
require_command "$HELM_BIN"
namespace="$(profile_namespace "$profile")"

helm_cmd status "$PLATFORM_RELEASE" --namespace "$namespace" >/dev/null || die "Helm release is not deployed: ${PLATFORM_RELEASE}/${namespace}"
kube -n "$namespace" rollout status deployment/ray-train-backend --timeout=5m
kube -n "$namespace" rollout status deployment/ray-train-frontend --timeout=5m

postgres_mode="$(profile_section_value "$profile" postgres mode)"
if [[ "$postgres_mode" == "standalone" ]]; then
  kube -n "$namespace" rollout status statefulset/postgres --timeout=5m
fi

if [[ "$(profile_section_value "$profile" availability pdbEnabled)" == "true" ]]; then
  kube -n "$namespace" get pdb ray-train-backend ray-train-frontend >/dev/null
fi
rendered="$(mktemp)"
trap 'rm -f "$rendered"' EXIT
render_profile "$profile" >"$rendered"
if has_rendered_kind "$rendered" HorizontalPodAutoscaler; then
  kube -n "$namespace" get hpa ray-train-backend ray-train-frontend >/dev/null
fi
while IFS= read -r ingress_name; do
  [[ -n "$ingress_name" ]] || continue
  kube -n "$namespace" get ingress "$ingress_name" >/dev/null
done < <(rendered_ingress_names "$rendered")
if [[ "$(profile_section_value "$profile" kueue manageResources)" == "true" ]]; then
  queue_name="$(profile_section_value "$profile" kueue clusterQueueName)"
  flavor_name="$(profile_section_value "$profile" kueue resourceFlavorName)"
  kube get clusterqueue "$queue_name" >/dev/null
  kube get resourceflavor "$flavor_name" >/dev/null
fi

kube -n "$namespace" get endpointslice -l kubernetes.io/service-name=ray-train-backend -o jsonpath='{range .items[*].endpoints[*]}{.conditions.ready}{"\n"}{end}' | grep -Fxq true || die "backend Service has no ready endpoint"
log "Control-plane verification passed."

if ! "$run_smoke"; then
  exit 0
fi
[[ -n "$api_url" ]] || die "--smoke requires --api-url"
[[ -n "$smoke_image" ]] || die "--smoke requires --smoke-image"
[[ -f "$env_file" ]] || die "--smoke requires --env-file with BOOTSTRAP_ADMIN_PASSWORD"
private_file_mode_ok "$env_file" || die "env file permissions are too open; run chmod 600 $env_file"
require_command python3
smoke_password="$(require_env_file_value "$env_file" BOOTSTRAP_ADMIN_PASSWORD)"
smoke_user="$(profile_section_value "$profile" backend bootstrapUsername)"
[[ -n "$smoke_user" ]] || smoke_user=admin

log "Submitting one-GPU smoke RayJob through ${api_url}."
API_URL="$api_url" PLATFORM_USER="$smoke_user" PLATFORM_PASSWORD="$smoke_password" IMAGE="$smoke_image" \
  WAIT_FOR_COMPLETION=true python3 "${PLATFORM_ROOT}/scripts/submit_smoke_job.py"
log "GPU smoke verification passed."
