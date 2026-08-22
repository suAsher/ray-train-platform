#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

usage() {
  cat <<'EOF'
Usage: ops/platform/deploy.sh --profile <profile.yaml> [--timeout 12m] [--verify-fsx-irsa] [--verify-idc-nfs]

Deploys exactly the reviewed profile with Helm --atomic --wait. It accepts no
--set values: change the profile under version control, then deploy it.
EOF
}

profile=""
timeout="12m"
verify_fsx_irsa=false
verify_idc_nfs=false
while (($#)); do
  case "$1" in
    --profile) profile="${2:-}"; shift 2 ;;
    --timeout) timeout="${2:-}"; shift 2 ;;
    --verify-fsx-irsa) verify_fsx_irsa=true; shift ;;
    --verify-idc-nfs) verify_idc_nfs=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

require_profile "$profile"
[[ "$timeout" =~ ^[0-9]+[smh]$ ]] || die "--timeout must look like 12m, 900s, or 1h"
require_command "$KUBECTL_BIN"
require_command "$HELM_BIN"

preflight_args=(--profile "$profile")
"$verify_fsx_irsa" && preflight_args+=(--verify-fsx-irsa)
"$verify_idc_nfs" && preflight_args+=(--verify-idc-nfs)
bash "${SCRIPT_DIR}/preflight.sh" "${preflight_args[@]}"

namespace="$(profile_namespace "$profile")"
ensure_platform_namespace "$namespace"

for secret in ray-platform-postgres ray-platform-pat ray-platform-bootstrap-admin; do
  kube -n "$namespace" get secret "$secret" >/dev/null || die "required Secret is absent: ${namespace}/${secret}; run bootstrap-secrets first"
done
tos_secret_name="$(profile_section_value "$profile" tos secretName)"
if [[ -n "$tos_secret_name" ]]; then
  kube -n "$namespace" get secret "$tos_secret_name" >/dev/null || die "TOS Secret is absent: ${namespace}/${tos_secret_name}"
fi
while IFS= read -r pull_secret; do
  [[ -n "$pull_secret" ]] || continue
  kube -n "$namespace" get secret "$pull_secret" >/dev/null || die "image pull Secret is absent: ${namespace}/${pull_secret}; create it with bootstrap-secrets or your secret manager"
done < <(profile_image_pull_secret_names "$profile")

log "Deploying release=${PLATFORM_RELEASE}, namespace=${namespace}, profile=${profile}"
helm_cmd upgrade --install "$PLATFORM_RELEASE" "$PLATFORM_CHART" \
  --namespace "$namespace" \
  --values "$profile" \
  --atomic --wait --timeout "$timeout"

revision="$(helm_cmd status "$PLATFORM_RELEASE" --namespace "$namespace" -o json 2>/dev/null | sed -n 's/.*"revision"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -n1)"
log "Deployment succeeded${revision:+ at Helm revision ${revision}}. Run ops/platform/verify.sh next."
