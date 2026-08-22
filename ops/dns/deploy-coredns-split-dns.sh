#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BLOCKS="${ROOT}/ops/dns/coredns-volcengine-forwarding.conf"
NAMESPACE="${COREDNS_NAMESPACE:-kube-system}"
CONFIGMAP="${COREDNS_CONFIGMAP:-coredns}"
DEPLOYMENT="${COREDNS_DEPLOYMENT:-coredns}"
BEGIN_MARKER="# BEGIN ray-train-platform managed Volcengine DNS forwarding"
END_MARKER="# END ray-train-platform managed Volcengine DNS forwarding"

usage() {
  cat <<'EOF'
Usage: deploy-coredns-split-dns.sh [--check|--apply|--revert]

Routes only Volcengine service zones through the VKE VPC resolvers
100.96.0.2 and 100.96.0.3. The existing root forwarder remains unchanged,
so IDC-only domains continue to use the cluster's existing IDC DNS path.
EOF
}

require_tools() {
  command -v kubectl >/dev/null || { echo "kubectl is required" >&2; exit 2; }
  command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }
}

read_corefile() {
  kubectl -n "$NAMESPACE" get configmap "$CONFIGMAP" -o jsonpath='{.data.Corefile}'
}

strip_managed_block() {
  awk -v begin="$BEGIN_MARKER" -v end="$END_MARKER" '
    $0 == begin { managed = 1; next }
    $0 == end { managed = 0; next }
    !managed { print }
  '
}

apply_corefile() {
  local corefile="$1"
  local patch_file="$2"
  jq -Rs '{data: {Corefile: .}}' "$corefile" >"$patch_file"
  kubectl -n "$NAMESPACE" patch configmap "$CONFIGMAP" --type merge --patch-file "$patch_file"
}

rollout_coredns() {
  kubectl -n "$NAMESPACE" rollout restart deployment/"$DEPLOYMENT"
  kubectl -n "$NAMESPACE" rollout status deployment/"$DEPLOYMENT" --timeout=180s
}

patch_corefile() {
  local desired="$1"
  local previous="$2"
  local patch_file="$3"

  apply_corefile "$desired" "$patch_file"
  if rollout_coredns; then
    return 0
  fi

  echo "CoreDNS rollout failed; restoring the previous Corefile" >&2
  apply_corefile "$previous" "$patch_file"
  kubectl -n "$NAMESPACE" rollout restart deployment/"$DEPLOYMENT"
  kubectl -n "$NAMESPACE" rollout status deployment/"$DEPLOYMENT" --timeout=180s
  return 1
}

check_config() {
  local current="$1"
  local zone
  grep -Fx "$BEGIN_MARKER" "$current" >/dev/null
  grep -Fx "$END_MARKER" "$current" >/dev/null
  for zone in ivolces.com volcengineapi.com tos-cn-shanghai.volces.com; do
    grep -F "${zone}:53" "$current" >/dev/null
  done
  test "$(grep -Fc 'forward . 100.96.0.2 100.96.0.3' "$current")" -eq 3
}

mode="${1:---check}"
case "$mode" in
  --check|--apply|--revert) ;;
  -h|--help) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac

require_tools
if [[ "$mode" != "--revert" ]]; then
  test -s "$BLOCKS" || { echo "managed CoreDNS blocks are missing: $BLOCKS" >&2; exit 2; }
fi
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
current="${workdir}/Corefile.current"
base="${workdir}/Corefile.base"
desired="${workdir}/Corefile.desired"
patch_file="${workdir}/patch.json"
read_corefile >"$current"

case "$mode" in
  --check)
    check_config "$current"
    kubectl -n "$NAMESPACE" rollout status deployment/"$DEPLOYMENT" --timeout=30s
    echo "CoreDNS split forwarding is configured"
    ;;
  --apply)
    strip_managed_block <"$current" >"$base"
    {
      cat "$BLOCKS"
      printf '\n'
      cat "$base"
    } >"$desired"
    patch_corefile "$desired" "$current" "$patch_file"
    read_corefile >"$current"
    check_config "$current"
    echo "CoreDNS split forwarding applied"
    ;;
  --revert)
    strip_managed_block <"$current" >"$desired"
    patch_corefile "$desired" "$current" "$patch_file"
    read_corefile >"$current"
    if grep -Fx "$BEGIN_MARKER" "$current" >/dev/null; then
      echo "managed CoreDNS block still exists after revert" >&2
      exit 1
    fi
    echo "CoreDNS split forwarding reverted"
    ;;
esac
