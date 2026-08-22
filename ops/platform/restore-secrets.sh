#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

usage() {
  cat <<'EOF'
Usage: ops/platform/restore-secrets.sh --profile <profile.yaml> --state-dir <backup-directory>

Restores only Secret manifests created by backup-state.sh. Run this after a
confirmed reset and before deploy so image pulls and existing local sessions
retain their original cryptographic keys. No TOS object data is read or copied.
EOF
}

profile=""
state_dir=""
while (($#)); do
  case "$1" in
    --profile) profile="${2:-}"; shift 2 ;;
    --state-dir) state_dir="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

require_profile "$profile"
[[ -d "$state_dir/secrets" ]] || die "state directory does not contain Secret manifests: $state_dir"
private_file_mode_ok "$state_dir" || die "state directory permissions are too open; run chmod 700 $state_dir"
require_command "$KUBECTL_BIN"
require_command jq

namespace="$(profile_namespace "$profile")"
ensure_platform_namespace "$namespace"

count=0
for manifest in "$state_dir"/secrets/*.yaml; do
  [[ -f "$manifest" ]] || continue
  # Recovery snapshots created before namespace pinning may contain the build
  # host's default namespace. Normalize every manifest on restore so the
  # profile, not kubeconfig context, is the sole namespace authority.
  kube create -f "$manifest" --dry-run=client -o json | jq --arg namespace "$namespace" '
    .metadata.namespace = $namespace
  ' | kube apply -f - >/dev/null
  count=$((count + 1))
done
(( count > 0 )) || die "no Secret manifests found in $state_dir/secrets"
log "Restored ${count} protected platform Secret manifest(s) into ${namespace}."
