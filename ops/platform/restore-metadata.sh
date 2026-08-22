#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

usage() {
  cat <<'EOF'
Usage: ops/platform/restore-metadata.sh --profile <profile.yaml> --input <backup.sql>

Restores a backup made by backup-metadata.sh into a fresh standalone test
PostgreSQL database. API and Portal are scaled to zero during restore, then
restored to the replica counts specified by the reviewed profile.
EOF
}

profile=""
input=""
while (($#)); do
  case "$1" in
    --profile) profile="${2:-}"; shift 2 ;;
    --input) input="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

require_profile "$profile"
[[ -f "$input" && -s "$input" ]] || die "--input must reference a non-empty backup file"
private_file_mode_ok "$input" || die "backup file permissions are too open; run chmod 600 $input"
require_command "$KUBECTL_BIN"

namespace="$(profile_namespace "$profile")"
postgres_mode="$(profile_section_value "$profile" postgres mode)"
[[ "$postgres_mode" == "standalone" ]] || die "metadata restore is only automated for postgres.mode=standalone"
backend_replicas="$(profile_section_value "$profile" backend replicaCount)"
frontend_replicas="$(profile_section_value "$profile" frontend replicaCount)"
[[ "$backend_replicas" =~ ^[0-9]+$ && "$frontend_replicas" =~ ^[0-9]+$ ]] || die "profile replicaCount values must be integers"

kube -n "$namespace" rollout status statefulset/postgres --timeout=5m
kube -n "$namespace" scale deployment/ray-train-backend --replicas=0
kube -n "$namespace" scale deployment/ray-train-frontend --replicas=0
kube -n "$namespace" rollout status deployment/ray-train-backend --timeout=5m
kube -n "$namespace" rollout status deployment/ray-train-frontend --timeout=5m
kube -n "$namespace" exec -i statefulset/postgres -- sh -ec 'exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <"$input"
kube -n "$namespace" scale deployment/ray-train-backend --replicas="$backend_replicas"
kube -n "$namespace" scale deployment/ray-train-frontend --replicas="$frontend_replicas"
kube -n "$namespace" rollout status deployment/ray-train-backend --timeout=5m
kube -n "$namespace" rollout status deployment/ray-train-frontend --timeout=5m
log "Metadata restore completed. Local accounts and platform records were restored; TOS objects were never accessed."
