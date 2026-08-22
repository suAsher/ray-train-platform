#!/usr/bin/env bash
# Shared helpers for portable platform delivery scripts. Profiles are limited
# to scalar lookups here, so the build host does not need yq.

set -euo pipefail

readonly PLATFORM_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly PLATFORM_CHART="${PLATFORM_ROOT}/helm/ray-train-platform"
readonly PLATFORM_RELEASE="${RAY_PLATFORM_RELEASE:-ray-platform}"
readonly KUBECTL_BIN="${KUBECTL:-kubectl}"
readonly HELM_BIN="${HELM:-helm}"
readonly PLATFORM_PART_OF_LABEL="app.kubernetes.io/part-of=ray-train-platform"
readonly PLATFORM_LEGACY_LABEL="app.kubernetes.io/managed-by=ray-train-platform"

log() {
  printf '[ray-platform] %s\n' "$*" >&2
}

die() {
  log "ERROR: $*"
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

kube() {
  "$KUBECTL_BIN" "$@"
}

helm_cmd() {
  "$HELM_BIN" "$@"
}

strip_yaml_scalar() {
  local value="$1"
  value="${value#${value%%[![:space:]]*}}"
  value="${value%${value##*[![:space:]]}}"
  if [[ "$value" =~ ^\".*\"$ || "$value" =~ ^\'.*\'$ ]]; then
    value="${value:1:${#value}-2}"
  fi
  printf '%s' "$value"
}

profile_top_value() {
  local profile="$1"
  local key="$2"
  local value
  value="$(awk -v key="$key" '$0 ~ ("^" key ":[[:space:]]*") {sub("^[^:]+:[[:space:]]*", ""); print; exit}' "$profile")"
  strip_yaml_scalar "$value"
}

profile_section_value() {
  local profile="$1"
  local section="$2"
  local key="$3"
  local value
  value="$(awk -v section="$section" -v key="$key" '
    $0 ~ ("^" section ":[[:space:]]*$") { inside = 1; next }
    inside && $0 ~ "^[^[:space:]]" { exit }
    inside && $0 ~ ("^[[:space:]]+" key ":[[:space:]]*") {
      sub("^[[:space:]]*[^:]+:[[:space:]]*", ""); print; exit
    }
  ' "$profile")"
  strip_yaml_scalar "$value"
}

# Profiles use the conventional Helm shape below. Keeping this parser small
# avoids making yq a mandatory dependency for the delivery host.
#
# global:
#   imagePullSecrets:
#     - name: registry-pull
profile_image_pull_secret_names() {
  local profile="$1"
  awk '
    /^global:[[:space:]]*$/ { inside_global = 1; next }
    inside_global && /^[^[:space:]]/ { exit }
    inside_global && /^[[:space:]]+imagePullSecrets:[[:space:]]*$/ { inside_secrets = 1; next }
    inside_secrets && /^[[:space:]]{2}[^[:space:]-][^:]*:[[:space:]]*/ { exit }
    inside_secrets && /^[[:space:]]+-[[:space:]]+name:[[:space:]]*/ {
      sub("^[[:space:]]*-[[:space:]]+name:[[:space:]]*", "")
      gsub(/^[[:space:]]+|[[:space:]]+$/, "")
      if ($0 ~ /^".*"$/ || $0 ~ /^\047.*\047$/) {
        $0 = substr($0, 2, length($0) - 2)
      }
      if ($0 != "") print
    }
  ' "$profile"
}

valid_dns_label() {
  [[ "$1" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ && ${#1} -le 63 ]]
}

profile_namespace() {
  local profile="$1"
  local namespace
  namespace="$(profile_top_value "$profile" namespace)"
  valid_dns_label "$namespace" || die "profile namespace must be a DNS label"
  case "$namespace" in
    default|kube-system|kube-public|kube-node-lease)
      die "refusing to operate on protected namespace: $namespace"
      ;;
  esac
  printf '%s' "$namespace"
}

require_profile() {
  local profile="$1"
  [[ -n "$profile" && -f "$profile" ]] || die "--profile must reference an existing YAML file"
}

render_profile() {
  local profile="$1"
  local namespace
  namespace="$(profile_namespace "$profile")"
  helm_cmd template "$PLATFORM_RELEASE" "$PLATFORM_CHART" --namespace "$namespace" --values "$profile"
}

ensure_platform_namespace() {
  local namespace="$1"
  kube create namespace "$namespace" --dry-run=client -o yaml | kube apply -f - >/dev/null
  kube label namespace "$namespace" \
    app.kubernetes.io/part-of=ray-train-platform \
    app.kubernetes.io/managed-by=ray-train-platform \
    --overwrite >/dev/null
}

rendered_env_value() {
  local rendered="$1"
  local name="$2"
  awk -v env_name="$name" '
    $0 ~ "name: " env_name "$" { want = 1; next }
    want && $0 ~ "value:" {
      sub(".*value: ", ""); gsub(/"/, ""); print; exit
    }
    want && $0 ~ "name:" { exit }
  ' "$rendered"
}

# Helm serializes JSON-valued environment variables as one quoted YAML string.
# Decode the complete JSON string rather than stripping quotation marks, which
# would turn a valid object into invalid pseudo-JSON before storage preflight.
rendered_env_json_value() {
  local rendered="$1"
  local name="$2"
  local value
  value="$(awk -v env_name="$name" '
    $0 ~ "name: " env_name "$" { want = 1; next }
    want && $0 ~ "value:" { sub(".*value: ", ""); print; exit }
    want && $0 ~ "name:" { exit }
  ' "$rendered")"
  [[ -n "$value" ]] || return 0
  require_command jq
  printf '%s' "$value" | jq -Rrc 'fromjson'
}

has_rendered_kind() {
  local rendered="$1"
  local kind="$2"
  grep -Fqx "kind: ${kind}" "$rendered"
}

private_file_mode_ok() {
  local file="$1"
  local mode
  if stat -c '%a' "$file" >/dev/null 2>&1; then
    mode="$(stat -c '%a' "$file")"
  else
    mode="$(stat -f '%Lp' "$file")"
  fi
  [[ $((8#$mode & 077)) -eq 0 ]]
}

env_file_value() {
  local file="$1"
  local key="$2"
  local value
  value="$(awk -v key="$key" '$0 ~ ("^" key "=") { print substr($0, length(key) + 2); exit }' "$file")"
  strip_yaml_scalar "$value"
}

require_env_file_value() {
  local file="$1"
  local key="$2"
  local value
  value="$(env_file_value "$file" "$key")"
  [[ -n "$value" ]] || die "missing required ${key} in env file"
  printf '%s' "$value"
}
