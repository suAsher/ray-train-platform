#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

usage() {
  cat <<'EOF'
Usage: ops/platform/bootstrap-alb.sh --profile <profile.yaml> [--timeout 15m]

Creates or adopts the dedicated private VKE ALB and then creates its
IngressClass. Network resources remain outside the Helm release so that ALB
provisioning is complete before platform Ingress routes are deployed.
EOF
}

profile=""
timeout="15m"
while (($#)); do
  case "$1" in
    --profile) profile="${2:-}"; shift 2 ;;
    --timeout) timeout="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

require_profile "$profile"
[[ "$timeout" =~ ^[0-9]+[smh]$ ]] || die "--timeout must look like 15m, 900s, or 1h"
require_command "$KUBECTL_BIN"
require_command "$HELM_BIN"
require_command awk
require_command date

rendered="$(mktemp)"
alb_manifest="$(mktemp)"
class_manifest="$(mktemp)"
trap 'rm -f "$rendered" "$alb_manifest" "$class_manifest"' EXIT

namespace="$(profile_namespace "$profile")"
helm_cmd template "$PLATFORM_RELEASE" "$PLATFORM_CHART" \
  --namespace "$namespace" \
  --values "$profile" \
  --set albInstance.enabled=true \
  --show-only templates/vke-alb-instance.yaml >"$rendered"

extract_kind() {
  local wanted="$1"
  local destination="$2"
  awk -v wanted="$wanted" '
    function flush_document() {
      if (kind == wanted) printf "%s", document
    }
    /^---$/ { flush_document(); document = ""; kind = ""; next }
    {
      document = document $0 "\n"
      # The IngressClass parameters also contain an indented `kind:` field.
      # Only a top-level YAML kind starts at column zero.
      if ($0 ~ /^kind: /) kind = $2
    }
    END { flush_document() }
  ' "$rendered" >"$destination"
  [[ -s "$destination" ]] || die "network render omitted kind: ${wanted}"
}

extract_kind "ALBInstance" "$alb_manifest"
extract_kind "IngressClass" "$class_manifest"

alb_name="$(awk '$1 == "name:" { print $2; exit }' "$alb_manifest")"
[[ -n "$alb_name" ]] || die "rendered ALBInstance has no metadata.name"

log "Applying dedicated private ALBInstance ${alb_name}."
kube apply -f "$alb_manifest"

timeout_seconds="${timeout%[smh]}"
case "$timeout" in
  *m) timeout_seconds=$((timeout_seconds * 60)) ;;
  *h) timeout_seconds=$((timeout_seconds * 3600)) ;;
esac
deadline=$(( $(date +%s) + timeout_seconds ))
while :; do
  phase="$(kube get albinstance "$alb_name" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  case "$phase" in
    Running)
      break
      ;;
    Deleting|Failed|Error)
      die "ALBInstance ${alb_name} entered phase ${phase}; inspect: kubectl describe albinstance ${alb_name}"
      ;;
  esac
  (( $(date +%s) < deadline )) || die "timed out waiting for ALBInstance ${alb_name} to reach Running"
  sleep 5
done

log "ALBInstance ${alb_name} is Running; applying its IngressClass."
kube apply -f "$class_manifest"
class_name="$(awk '$1 == "name:" { print $2; exit }' "$class_manifest")"
kube get ingressclass "$class_name" >/dev/null
log "Private ALB network bootstrap succeeded: class=${class_name}."
