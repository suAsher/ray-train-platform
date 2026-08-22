#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

usage() {
  cat <<'EOF'
Usage: ops/platform/backup-state.sh --profile <profile.yaml> --output-dir <new-directory> [--secret <name>]...

Creates a protected local recovery snapshot before a platform reset:
  - PostgreSQL platform metadata (local accounts, tenants, catalog, history)
  - named platform Secret manifests (base64-encoded Kubernetes data)

It never reads TOS, IDC, Loki, KubeRay or tenant workload data. Add --secret
for an environment-specific image-pull Secret, for example --secret registry.
EOF
}

profile=""
output_dir=""
extra_secrets=""
while (($#)); do
  case "$1" in
    --profile) profile="${2:-}"; shift 2 ;;
    --output-dir) output_dir="${2:-}"; shift 2 ;;
    --secret) extra_secrets="${extra_secrets}${extra_secrets:+,}${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

require_profile "$profile"
[[ -n "$output_dir" ]] || die "--output-dir is required"
[[ ! -e "$output_dir" ]] || die "refusing to overwrite existing state directory: $output_dir"
require_command "$KUBECTL_BIN"

namespace="$(profile_namespace "$profile")"
database_secret="$(profile_section_value "$profile" postgres existingSecret)"
[[ -n "$database_secret" ]] || database_secret=ray-platform-postgres
tos_secret="$(profile_section_value "$profile" tos secretName)"
secrets="${database_secret},ray-platform-pat,ray-platform-bootstrap-admin"
[[ -n "$tos_secret" ]] && secrets="${secrets},${tos_secret}"
[[ -n "$extra_secrets" ]] && secrets="${secrets},${extra_secrets}"

IFS=',' read -r -a secret_list <<<"$secrets"
for secret_name in "${secret_list[@]}"; do
  valid_dns_label "$secret_name" || die "invalid Secret name: $secret_name"
  kube -n "$namespace" get secret "$secret_name" >/dev/null || die "required platform Secret is absent: ${namespace}/${secret_name}"
done

umask 077
mkdir -p "$output_dir/secrets"
bash "${SCRIPT_DIR}/backup-metadata.sh" --profile "$profile" --output "$output_dir/metadata.sql"
for secret_name in "${secret_list[@]}"; do
  # Strip server-assigned metadata so the manifest can be applied into a fresh
  # namespace after reset. Secret data remain encoded and never enter stdout.
  kube -n "$namespace" get secret "$secret_name" -o json | jq --arg namespace "$namespace" '
    del(.metadata.namespace, .metadata.uid, .metadata.resourceVersion,
        .metadata.creationTimestamp, .metadata.managedFields,
        .metadata.annotations."kubectl.kubernetes.io/last-applied-configuration")
    | .metadata.namespace = $namespace
  ' | kube -n "$namespace" create -f - --dry-run=client -o yaml >"$output_dir/secrets/${secret_name}.yaml"
done

printf '%s\n' "$profile" >"$output_dir/profile-path.txt"
printf '%s\n' "$namespace" >"$output_dir/namespace.txt"
log "Platform state backup created at ${output_dir}. Keep it outside the repository and chmod 700 it."
