#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

usage() {
  cat <<'EOF'
Usage: ops/platform/preflight.sh --profile <profile.yaml> [--verify-fsx-irsa] [--verify-idc-nfs]

Prerequisite check. Without storage verification flags it is read-only.
Enabling governed TOS or IDC data spaces requires the matching explicit flag;
that verification creates isolated temporary probes and removes them on exit.
EOF
}

profile=""
verify_fsx_irsa=false
verify_idc_nfs=false
while (($#)); do
  case "$1" in
    --profile) profile="${2:-}"; shift 2 ;;
    --verify-fsx-irsa) verify_fsx_irsa=true; shift ;;
    --verify-idc-nfs) verify_idc_nfs=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

require_profile "$profile"
require_command "$KUBECTL_BIN"
require_command "$HELM_BIN"

namespace="$(profile_namespace "$profile")"
rendered="$(mktemp)"
trap 'rm -f "$rendered"' EXIT

# The production cache chart install contract fixes both independent disk
# provisioners. Keep the platform gate tied to those releases instead of
# probing arbitrary workloads.
readonly CACHE_PROVISIONER_NAMESPACE="ray-cache-local"

rendered_defines_ingress_class() {
  local rendered_file="$1"
  local expected_class="$2"
  awk -v expected="$expected_class" '
    $1 == "kind:" && $2 == "IngressClass" { in_resource = 1; next }
    in_resource && $1 == "name:" && $2 == expected { found = 1; exit }
    $1 == "---" { in_resource = 0 }
    END { exit found ? 0 : 1 }
  ' "$rendered_file"
}

verify_local_cache() {
  local storage_class="$1"
  local expected_provisioner="$2"
  local expected_release="$3"
  local expected_deployment="$4"
  local provisioner binding_mode reclaim_policy
  local stable_default beta_default allow_expansion release_label available_condition

  [[ -n "$storage_class" ]] || die "local cache is enabled but LOCAL_CACHE_STORAGE_CLASS is empty"

  if ! provisioner="$(kube get storageclass "$storage_class" -o jsonpath='{.provisioner}')"; then
    die "local cache StorageClass is absent: $storage_class"
  fi
  [[ "$provisioner" == "$expected_provisioner" ]] || \
    die "local cache StorageClass ${storage_class} has provisioner=${provisioner:-<empty>}, expected ${expected_provisioner}"

  binding_mode="$(kube get storageclass "$storage_class" -o jsonpath='{.volumeBindingMode}')"
  [[ "$binding_mode" == "WaitForFirstConsumer" ]] || \
    die "local cache StorageClass ${storage_class} has volumeBindingMode=${binding_mode:-<empty>}, expected WaitForFirstConsumer"

  reclaim_policy="$(kube get storageclass "$storage_class" -o jsonpath='{.reclaimPolicy}')"
  [[ "$reclaim_policy" == "Delete" ]] || \
    die "local cache StorageClass ${storage_class} has reclaimPolicy=${reclaim_policy:-<empty>}, expected Delete"

  stable_default="$(kube get storageclass "$storage_class" -o jsonpath='{.metadata.annotations.storageclass\.kubernetes\.io/is-default-class}')"
  [[ -z "$stable_default" || "$stable_default" == "false" ]] || \
    die "local cache StorageClass ${storage_class} must not be default via storageclass.kubernetes.io/is-default-class"

  beta_default="$(kube get storageclass "$storage_class" -o jsonpath='{.metadata.annotations.storageclass\.beta\.kubernetes\.io/is-default-class}')"
  [[ -z "$beta_default" || "$beta_default" == "false" ]] || \
    die "local cache StorageClass ${storage_class} must not be default via storageclass.beta.kubernetes.io/is-default-class"

  allow_expansion="$(kube get storageclass "$storage_class" -o jsonpath='{.allowVolumeExpansion}')"
  [[ -z "$allow_expansion" || "$allow_expansion" == "false" ]] || \
    die "local cache StorageClass ${storage_class} must not allow volume expansion"

  if ! release_label="$(kube -n "$CACHE_PROVISIONER_NAMESPACE" get deployment "$expected_deployment" \
    -o jsonpath='{.metadata.labels.app\.kubernetes\.io/instance}')"; then
    die "cache provisioner Deployment is absent: ${CACHE_PROVISIONER_NAMESPACE}/${expected_deployment}"
  fi
  [[ "$release_label" == "$expected_release" ]] || \
    die "cache provisioner Deployment ${CACHE_PROVISIONER_NAMESPACE}/${expected_deployment} belongs to Helm release ${release_label:-<empty>}, expected Helm release ${expected_release}"

  available_condition="$(kube -n "$CACHE_PROVISIONER_NAMESPACE" get deployment "$expected_deployment" \
    -o jsonpath='{.status.conditions[?(@.type=="Available")].status}')"
  [[ "$available_condition" == "True" ]] || \
    die "cache provisioner Deployment is not Ready: ${CACHE_PROVISIONER_NAMESPACE}/${expected_deployment}"
}

helm_cmd lint "$PLATFORM_CHART" --values "$profile" >/dev/null
render_profile "$profile" >"$rendered"

local_cache_enabled="$(rendered_env_value "$rendered" LOCAL_CACHE_ENABLED)"
case "$local_cache_enabled" in
  true)
    verify_local_cache "$(rendered_env_value "$rendered" LOCAL_CACHE_STORAGE_CLASS_DATA1)" \
      "wellspiking.ai/local-path-data1" "ray-cache-local-data1" "ray-cache-local-data1"
    verify_local_cache "$(rendered_env_value "$rendered" LOCAL_CACHE_STORAGE_CLASS_DATA2)" \
      "wellspiking.ai/local-path-data2" "ray-cache-local-data2" "ray-cache-local-data2"
    ;;
  false|"")
    ;;
  *)
    die "LOCAL_CACHE_ENABLED must render as true or false, got: $local_cache_enabled"
    ;;
esac

for resource in rayjobs.ray.io rayclusters.ray.io; do
  kube get crd "$resource" >/dev/null || die "required KubeRay CRD is absent: $resource"
done
for resource in clusterqueues.kueue.x-k8s.io localqueues.kueue.x-k8s.io; do
  kube get crd "$resource" >/dev/null || die "required Kueue CRD is absent: $resource"
done

selector="$(rendered_env_value "$rendered" TRAINING_NODE_SELECTOR)"
[[ -n "$selector" ]] || die "profile did not render TRAINING_NODE_SELECTOR"
gpu_nodes="$(kube get nodes -l "$selector" -o name)"
[[ -n "$gpu_nodes" ]] || die "no node matches training.nodeSelector=${selector}"
gpu_capacity="$(kube get nodes -l "$selector" -o jsonpath='{range .items[*]}{.status.allocatable.nvidia\.com/gpu}{"\n"}{end}')"
grep -Eq '[1-9][0-9]*' <<<"$gpu_capacity" || die "matching training nodes expose no allocatable nvidia.com/gpu"

if has_rendered_kind "$rendered" Ingress; then
  while IFS= read -r ingress_class; do
    [[ -n "$ingress_class" ]] || continue
    if kube get ingressclass "$ingress_class" >/dev/null 2>&1; then
      continue
    fi
    if rendered_defines_ingress_class "$rendered" "$ingress_class"; then
      log "IngressClass ${ingress_class} is managed by this release and will be created by Helm."
      continue
    fi
    die "IngressClass is absent: $ingress_class"
  done < <(awk '$1 == "ingressClassName:" {print $2}' "$rendered" | sort -u)
fi

if [[ "$(rendered_env_value "$rendered" DATA_SPACES_ENABLED)" == "true" ]]; then
  "$verify_fsx_irsa" || die "dataSpaces.enabled requires --verify-fsx-irsa and a passing FSX prefix-isolation verification"
  verifier="${PLATFORM_ROOT}/ops/storage/shanghai-data-transfer/41-verify-irsa-prefix-mount.sh"
  [[ -x "$verifier" ]] || die "FSX verifier is unavailable or not executable: $verifier"
  data_space_attributes="$(rendered_env_json_value "$rendered" DATA_SPACES_FSX_VOLUME_ATTRIBUTES_JSON)"
  fsx_server="$(printf '%s' "$data_space_attributes" | jq -r '.server // empty')"
  fsx_region="$(printf '%s' "$data_space_attributes" | jq -r '.region // empty')"
  fsx_bucket="$(printf '%s' "$data_space_attributes" | jq -r '.bucket // empty')"
  # FSX is a CSI/IRSA mount check, not a private-workspace image check. Keep
  # its probe image independently pullable so a new registry credential cannot
  # block validation before tenant namespaces exist. Operators may override it
  # for isolated environments through FSX_SMOKE_IMAGE.
  smoke_image="${FSX_SMOKE_IMAGE:-harbor.wellspiking.ai/hub/library/busybox:1.36}"
  [[ -n "$fsx_server" && -n "$fsx_region" && -n "$fsx_bucket" ]] || die "dataSpaces.fsxVolumeAttributes must contain server, region, and bucket"
  [[ -n "$smoke_image" ]] || die "FSX_SMOKE_IMAGE must not be empty"
  FSX_TOS_SERVER="$fsx_server" FSX_TOS_REGION="$fsx_region" FSX_TOS_BUCKET="$fsx_bucket" TRAINING_NODE_SELECTOR="$selector" SMOKE_IMAGE="$smoke_image" "$verifier"
fi

if [[ "$(rendered_env_value "$rendered" IDC_DATA_SPACES_ENABLED)" == "true" ]]; then
  "$verify_idc_nfs" || die "idcDataSpaces.enabled requires --verify-idc-nfs and a passing read-only NFS verification"
  verifier="${PLATFORM_ROOT}/ops/storage/idc-nfs/41-verify-readonly-mount.sh"
  [[ -x "$verifier" ]] || die "IDC verifier is unavailable or not executable: $verifier"
  IDC_DATA_SPACES_SOURCES_JSON="$(rendered_env_json_value "$rendered" IDC_DATA_SPACES_SOURCES_JSON)" \
    TRAINING_NODE_SELECTOR="$selector" "$verifier"
fi

for secret in ray-platform-postgres ray-platform-pat ray-platform-bootstrap-admin; do
  if ! kube -n "$namespace" get secret "$secret" >/dev/null 2>&1; then
    log "required Secret is not present yet: ${namespace}/${secret} (run bootstrap-secrets before deploy)"
  fi
done

log "preflight passed: namespace=${namespace}, training selector=${selector}"
