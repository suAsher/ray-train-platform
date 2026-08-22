#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

usage() {
  cat <<'EOF'
Usage: ops/platform/bootstrap-secrets.sh --profile <profile.yaml> --env-file <secret.env>

Creates only the platform's named Kubernetes Secrets. The env file must be
owner-readable only (chmod 600); values are never printed or persisted in the
profile.
EOF
}

profile=""
env_file=""
while (($#)); do
  case "$1" in
    --profile) profile="${2:-}"; shift 2 ;;
    --env-file) env_file="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

require_profile "$profile"
[[ -f "$env_file" ]] || die "--env-file must reference an existing file"
private_file_mode_ok "$env_file" || die "env file permissions are too open; run chmod 600 $env_file"
require_command "$KUBECTL_BIN"

namespace="$(profile_namespace "$profile")"
postgres_mode="$(profile_section_value "$profile" postgres mode)"
[[ -n "$postgres_mode" ]] || postgres_mode="external"

database_url="$(require_env_file_value "$env_file" DATABASE_URL)"
pat_pepper="$(require_env_file_value "$env_file" PAT_PEPPER)"
bootstrap_password="$(require_env_file_value "$env_file" BOOTSTRAP_ADMIN_PASSWORD)"
[[ ${#pat_pepper} -ge 32 ]] || die "PAT_PEPPER must contain at least 32 bytes"

temporary_dir="$(mktemp -d)"
trap 'rm -rf "$temporary_dir"' EXIT
umask 077
printf '%s' "$database_url" >"${temporary_dir}/DATABASE_URL"
printf '%s' "$pat_pepper" >"${temporary_dir}/PAT_PEPPER"
printf '%s' "$bootstrap_password" >"${temporary_dir}/BOOTSTRAP_ADMIN_PASSWORD"

ensure_platform_namespace "$namespace"
if [[ "$postgres_mode" == "standalone" ]]; then
  postgres_user="$(require_env_file_value "$env_file" POSTGRES_USER)"
  postgres_password="$(require_env_file_value "$env_file" POSTGRES_PASSWORD)"
  printf '%s' "$postgres_user" >"${temporary_dir}/POSTGRES_USER"
  printf '%s' "$postgres_password" >"${temporary_dir}/POSTGRES_PASSWORD"
  kube -n "$namespace" create secret generic ray-platform-postgres \
    --from-file=DATABASE_URL="${temporary_dir}/DATABASE_URL" \
    --from-file=POSTGRES_USER="${temporary_dir}/POSTGRES_USER" \
    --from-file=POSTGRES_PASSWORD="${temporary_dir}/POSTGRES_PASSWORD" \
    --dry-run=client -o yaml | kube apply -f - >/dev/null
else
  kube -n "$namespace" create secret generic ray-platform-postgres \
    --from-file=DATABASE_URL="${temporary_dir}/DATABASE_URL" \
    --dry-run=client -o yaml | kube apply -f - >/dev/null
fi

kube -n "$namespace" create secret generic ray-platform-pat \
  --from-file=PAT_PEPPER="${temporary_dir}/PAT_PEPPER" \
  --dry-run=client -o yaml | kube apply -f - >/dev/null
kube -n "$namespace" create secret generic ray-platform-bootstrap-admin \
  --from-file=BOOTSTRAP_ADMIN_PASSWORD="${temporary_dir}/BOOTSTRAP_ADMIN_PASSWORD" \
  --dry-run=client -o yaml | kube apply -f - >/dev/null

tos_secret_name="$(profile_section_value "$profile" tos secretName)"
tos_access_key="$(env_file_value "$env_file" TOS_ACCESS_KEY)"
tos_secret_key="$(env_file_value "$env_file" TOS_SECRET_KEY)"
if [[ -n "$tos_secret_name" || -n "$tos_access_key" || -n "$tos_secret_key" ]]; then
  [[ -n "$tos_secret_name" && -n "$tos_access_key" && -n "$tos_secret_key" ]] || die "TOS Secret name, access key, and secret key must be supplied together"
  printf '%s' "$tos_access_key" >"${temporary_dir}/access-key"
  printf '%s' "$tos_secret_key" >"${temporary_dir}/secret-key"
  tos_args=(--from-file=access-key="${temporary_dir}/access-key" --from-file=secret-key="${temporary_dir}/secret-key")
  tos_token="$(env_file_value "$env_file" TOS_SECURITY_TOKEN)"
  if [[ -n "$tos_token" ]]; then
    printf '%s' "$tos_token" >"${temporary_dir}/security-token"
    tos_args+=(--from-file=security-token="${temporary_dir}/security-token")
  fi
  kube -n "$namespace" create secret generic "$tos_secret_name" "${tos_args[@]}" --dry-run=client -o yaml | kube apply -f - >/dev/null
fi

registry_server="$(env_file_value "$env_file" REGISTRY_SERVER)"
registry_username="$(env_file_value "$env_file" REGISTRY_USERNAME)"
registry_password="$(env_file_value "$env_file" REGISTRY_PASSWORD)"
registry_secret_name="$(env_file_value "$env_file" REGISTRY_SECRET_NAME)"
if [[ -n "$registry_server" || -n "$registry_username" || -n "$registry_password" || -n "$registry_secret_name" ]]; then
  [[ -n "$registry_server" && -n "$registry_username" && -n "$registry_password" ]] || die "REGISTRY_SERVER, REGISTRY_USERNAME, and REGISTRY_PASSWORD must be supplied together"

  configured_pull_secrets="$(profile_image_pull_secret_names "$profile")"
  if [[ -z "$registry_secret_name" ]]; then
    configured_count="$(printf '%s\n' "$configured_pull_secrets" | sed '/^$/d' | wc -l | tr -d '[:space:]')"
    [[ "$configured_count" == "1" ]] || die "REGISTRY_SECRET_NAME is required unless the profile configures exactly one global.imagePullSecrets entry"
    registry_secret_name="$(printf '%s\n' "$configured_pull_secrets" | sed '/^$/d' | head -n 1)"
  fi
  valid_dns_label "$registry_secret_name" || die "REGISTRY_SECRET_NAME must be a DNS label"
  if ! printf '%s\n' "$configured_pull_secrets" | grep -Fxq -- "$registry_secret_name"; then
    die "REGISTRY_SECRET_NAME must be listed in global.imagePullSecrets"
  fi

  kube -n "$namespace" create secret docker-registry "$registry_secret_name" \
    --docker-server="$registry_server" \
    --docker-username="$registry_username" \
    --docker-password="$registry_password" \
    --dry-run=client -o yaml | kube apply -f - >/dev/null
fi

log "Secrets are ready in ${namespace}; no secret values were printed."
