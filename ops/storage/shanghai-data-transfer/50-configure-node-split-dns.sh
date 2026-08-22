#!/usr/bin/env bash
set -euo pipefail

# Configure systemd-resolved so only Volcengine service domains use the VKE
# VPC resolvers.  Existing per-link IDC DNS remains responsible for every
# other domain, including personal IDC storage hosts.

readonly DROP_IN_DIR="/etc/systemd/resolved.conf.d"
readonly DROP_IN_FILE="${DROP_IN_DIR}/60-ray-platform-tos-routing.conf"
readonly VKE_DNS_PRIMARY="${VKE_DNS_PRIMARY:-100.96.0.2}"
readonly VKE_DNS_SECONDARY="${VKE_DNS_SECONDARY:-100.96.0.3}"
readonly TOS_TEST_HOST="${TOS_TEST_HOST:-shanghai-data-transfer.tos-cn-shanghai.ivolces.com}"
readonly TOS_API_TEST_HOST="${TOS_API_TEST_HOST:-tos-cn-shanghai.ivolces.com}"

usage() {
  cat <<'EOF'
Usage: 50-configure-node-split-dns.sh [--check|--apply|--revert]

Adds a systemd-resolved routing-only DNS configuration:
  *.tos-cn-shanghai.ivolces.com     -> VKE VPC DNS
  *.tos-s3-cn-shanghai.ivolces.com  -> VKE VPC DNS
  *.volcengineapi.com               -> VKE VPC DNS

All other domains retain the node's existing per-link DNS configuration.
Run this on every non-virtual node that can host FSX/TOS-backed Pods.
EOF
}

require_root() {
  [[ "$(id -u)" == "0" ]] || { echo "must run as root" >&2; exit 2; }
}

require_resolved() {
  command -v resolvectl >/dev/null || { echo "resolvectl is required" >&2; exit 2; }
  command -v systemctl >/dev/null || { echo "systemctl is required" >&2; exit 2; }
  systemctl is-active --quiet systemd-resolved || { echo "systemd-resolved is not active" >&2; exit 2; }
}

render_drop_in() {
  printf '%s\n' \
    '[Resolve]' \
    '# Route only Volcengine object-storage and STS domains to the VKE VPC DNS.' \
    '# Do not add ~. here: IDC domains must continue to use the node link DNS.' \
    "DNS=${VKE_DNS_PRIMARY} ${VKE_DNS_SECONDARY}" \
    'Domains=~tos-cn-shanghai.ivolces.com ~tos-s3-cn-shanghai.ivolces.com ~volcengineapi.com'
}

verify_tos_resolution() {
  local host
  local answer
  for host in "$TOS_API_TEST_HOST" "$TOS_TEST_HOST"; do
    answer="$(timeout 10 getent ahostsv4 "$host" 2>&1)" || {
      echo "TOS routing verification failed for ${host}" >&2
      printf '%s\n' "$answer" >&2
      return 1
    }
    printf '%s\n' "$answer" | grep -Eq '100\.(64|96)\.' || {
      echo "TOS routing verification returned no VKE private address for ${host}" >&2
      printf '%s\n' "$answer" >&2
      return 1
    }
  done
}

mode="check"
case "${1:---check}" in
  --check) mode="check" ;;
  --apply) mode="apply" ;;
  --revert) mode="revert" ;;
  -h|--help) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac

require_resolved

case "$mode" in
  check)
    [[ -f "$DROP_IN_FILE" ]] || { echo "split DNS is not configured: ${DROP_IN_FILE}" >&2; exit 1; }
    grep -Fx "DNS=${VKE_DNS_PRIMARY} ${VKE_DNS_SECONDARY}" "$DROP_IN_FILE" >/dev/null
    grep -Fx 'Domains=~tos-cn-shanghai.ivolces.com ~tos-s3-cn-shanghai.ivolces.com ~volcengineapi.com' "$DROP_IN_FILE" >/dev/null
    verify_tos_resolution
    echo "split DNS is configured and TOS routing is healthy"
    ;;
  apply)
    require_root
    install -d -m 0755 "$DROP_IN_DIR"
    temp_file="$(mktemp)"
    trap 'rm -f "$temp_file"' EXIT
    render_drop_in >"$temp_file"
    install -m 0644 "$temp_file" "$DROP_IN_FILE"
    systemctl restart systemd-resolved
    verify_tos_resolution
    echo "split DNS applied: ${DROP_IN_FILE}"
    ;;
  revert)
    require_root
    rm -f "$DROP_IN_FILE"
    systemctl restart systemd-resolved
    echo "split DNS reverted"
    ;;
esac
