#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

usage() {
  cat <<'EOF'
Usage: ops/platform/backup-metadata.sh --profile <profile.yaml> --output <backup.sql>

Exports only the platform PostgreSQL metadata database from a standalone test
PostgreSQL Pod. This does not read or copy TOS, IDC, Loki, KubeRay, or any
tenant workload data. The backup includes local accounts, tenant metadata,
image catalog and task history; protect it as confidential data.
EOF
}

profile=""
output=""
while (($#)); do
  case "$1" in
    --profile) profile="${2:-}"; shift 2 ;;
    --output) output="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

require_profile "$profile"
[[ -n "$output" ]] || die "--output is required"
[[ ! -e "$output" ]] || die "refusing to overwrite existing backup: $output"
require_command "$KUBECTL_BIN"

namespace="$(profile_namespace "$profile")"
postgres_mode="$(profile_section_value "$profile" postgres mode)"
[[ "$postgres_mode" == "standalone" ]] || die "metadata backup is only automated for postgres.mode=standalone; use the external database backup procedure"
parent_dir="$(dirname "$output")"
[[ -d "$parent_dir" ]] || die "backup output directory does not exist: $parent_dir"

umask 077
kube -n "$namespace" exec statefulset/postgres -- sh -ec 'exec pg_dump --clean --if-exists -U "$POSTGRES_USER" -d "$POSTGRES_DB"' >"$output"
[[ -s "$output" ]] || { rm -f "$output"; die "PostgreSQL backup is empty"; }
log "Metadata backup created at ${output}. Keep it outside the repository and restrict its permissions."
