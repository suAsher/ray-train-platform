#!/usr/bin/env bash
set -euo pipefail

readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly KUBECTL_BIN="${KUBECTL_BIN:-kubectl}"
readonly HELM_BIN="${HELM_BIN:-helm}"
readonly CURL_BIN="${CURL_BIN:-curl}"
readonly SHA256_BIN="${SHA256_BIN:-shasum}"
readonly KUBERAY_BACKUP_BIN="${KUBERAY_BACKUP_BIN:-${script_dir}/backup.sh}"
readonly KUBERAY_TARGET_VERSION='1.6.2'
readonly KUBERAY_NAMESPACE="${KUBERAY_NAMESPACE:-kuberay-system}"
readonly KUBERAY_RELEASE="${KUBERAY_RELEASE:-kuberay-operator}"
readonly KUBERAY_CHART_URL="${KUBERAY_CHART_URL:-https://github.com/ray-project/kuberay-helm/releases/download/kuberay-operator-1.6.2/kuberay-operator-1.6.2.tgz}"
readonly KUBERAY_OFFICIAL_CHART_URL='https://github.com/ray-project/kuberay-helm/releases/download/kuberay-operator-1.6.2/kuberay-operator-1.6.2.tgz'
readonly KUBERAY_CRD_BASE_URL='https://raw.githubusercontent.com/ray-project/kuberay/598eb66/ray-operator/config/crd/bases'
readonly KUBERAY_CHART_CHECKSUM_FILE="${script_dir}/checksums/kuberay-operator-1.6.2.sha256"
readonly KUBERAY_OPERATOR_REPOSITORY='quay.io/kuberay/operator'
readonly KUBERAY_OPERATOR_TAG='v1.6.2'
readonly PLATFORM_NAMESPACE="${PLATFORM_NAMESPACE:-ray-train-platform}"
readonly PLATFORM_BACKEND_DEPLOYMENT="${PLATFORM_BACKEND_DEPLOYMENT:-ray-train-backend}"
readonly KUBERAY_BACKUP_PARENT="${KUBERAY_BACKUP_PARENT:-}"
readonly EXPECTED_KUBERAY_CRD_SHA256="${EXPECTED_KUBERAY_CRD_SHA256:-}"
readonly EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST="${EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST:-}"
readonly MAINTENANCE_WAIT_ATTEMPTS="${MAINTENANCE_WAIT_ATTEMPTS:-30}"
readonly MAINTENANCE_WAIT_INTERVAL_SECONDS="${MAINTENANCE_WAIT_INTERVAL_SECONDS:-2}"

artifact_dir=''
maintenance_active=false
queues_patched=false
backend_replicas=''
queue_state_file=''
target_context=''

die() {
  echo "KubeRay upgrade failed: $*" >&2
  exit 1
}

kubectl() {
  command "$KUBECTL_BIN" "$@"
}

helm() {
  command "$HELM_BIN" "$@"
}

validate_name() {
  [[ "$1" =~ ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$ ]] || die "unsafe ${2}: $1"
}

sha256_file() {
  local file="$1" output digest
  case "$(basename -- "$SHA256_BIN")" in
    sha256sum) output="$(command "$SHA256_BIN" "$file")" ;;
    *) output="$(command "$SHA256_BIN" -a 256 "$file")" ;;
  esac
  digest="${output%%[[:space:]]*}"
  [[ "$digest" =~ ^[0-9a-f]{64}$ ]] || die "invalid SHA-256 output for $(basename -- "$file")"
  printf '%s\n' "$digest"
}

wait_for_backend_replicas() {
  local expected="$1" observed desired current ready available attempt
  for ((attempt = 1; attempt <= MAINTENANCE_WAIT_ATTEMPTS; attempt++)); do
    observed="$(kubectl get deployment "$PLATFORM_BACKEND_DEPLOYMENT" --context "$target_context" -n "$PLATFORM_NAMESPACE" -o jsonpath='{.spec.replicas}{" "}{.status.replicas}{" "}{.status.readyReplicas}{" "}{.status.availableReplicas}{"\n"}')" || return 1
    read -r desired current ready available <<<"$observed"
    current="${current:-0}"
    ready="${ready:-0}"
    available="${available:-0}"
    if [[ "$desired" == "$expected" ]]; then
      if [[ "$expected" == 0 && "$current" == 0 && "$ready" == 0 && "$available" == 0 ]]; then return 0; fi
      if [[ "$expected" != 0 && "$current" == "$expected" && "$ready" == "$expected" && "$available" == "$expected" ]]; then return 0; fi
    fi
    sleep "$MAINTENANCE_WAIT_INTERVAL_SECONDS"
  done
  return 1
}

restore_maintenance() {
  local failed=0 name policy patch
  if [[ -n "$backend_replicas" ]]; then
    kubectl scale "deployment/${PLATFORM_BACKEND_DEPLOYMENT}" --replicas="$backend_replicas" --context "$target_context" -n "$PLATFORM_NAMESPACE" >/dev/null || failed=1
    wait_for_backend_replicas "$backend_replicas" || failed=1
  fi
  if [[ "$queues_patched" == true && -s "$queue_state_file" ]]; then
    while IFS=$'\t' read -r name policy; do
      [[ -n "$name" ]] || continue
      if [[ "$policy" == __ABSENT__ || "$policy" == None ]]; then
        patch='{"spec":{"stopPolicy":null}}'
      else
        patch="{\"spec\":{\"stopPolicy\":\"${policy}\"}}"
      fi
      kubectl patch clusterqueue "$name" --type merge -p "$patch" --context "$target_context" >/dev/null || failed=1
      if [[ "$policy" == __ABSENT__ || "$policy" == None ]]; then
        kubectl wait --for=condition=Active=true "clusterqueue/${name}" --timeout=120s --context "$target_context" >/dev/null || failed=1
      fi
    done <"$queue_state_file"
  fi
  return "$failed"
}

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ "$maintenance_active" == true ]]; then
    restore_maintenance || status=1
  fi
  if [[ -n "$artifact_dir" && -d "$artifact_dir" ]]; then
    rm -rf -- "$artifact_dir"
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

validate_name "$KUBERAY_NAMESPACE" KUBERAY_NAMESPACE
validate_name "$KUBERAY_RELEASE" KUBERAY_RELEASE
validate_name "$PLATFORM_NAMESPACE" PLATFORM_NAMESPACE
validate_name "$PLATFORM_BACKEND_DEPLOYMENT" PLATFORM_BACKEND_DEPLOYMENT
[[ "${CONFIRM_KUBERAY_UPGRADE:-0}" == 1 ]] || die 'set CONFIRM_KUBERAY_UPGRADE=1 after reviewing the maintenance and rollback procedure'
[[ "$KUBERAY_CHART_URL" == "$KUBERAY_OFFICIAL_CHART_URL" ]] || die 'KUBERAY_CHART_URL must be the fixed official KubeRay 1.6.2 release URL'
[[ "$EXPECTED_KUBERAY_CRD_SHA256" =~ ^[0-9a-f]{64}$ ]] || die 'EXPECTED_KUBERAY_CRD_SHA256 must be an audited 64-character lowercase SHA-256'
[[ "${CONFIRM_KUBERAY_CRD_SHA256:-}" == "$EXPECTED_KUBERAY_CRD_SHA256" ]] || die 'CONFIRM_KUBERAY_CRD_SHA256 must exactly match the audited CRD digest'
[[ "$EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST" =~ ^[0-9a-f]{64}$ ]] || die 'EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST must be an audited 64-character lowercase SHA-256'
[[ "${CONFIRM_KUBERAY_OPERATOR_IMAGE_DIGEST:-}" == "$EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST" ]] || die 'CONFIRM_KUBERAY_OPERATOR_IMAGE_DIGEST must exactly match the expected running image digest'
[[ "$KUBERAY_BACKUP_PARENT" == /* && -d "$KUBERAY_BACKUP_PARENT" && ! -L "$KUBERAY_BACKUP_PARENT" ]] || die 'KUBERAY_BACKUP_PARENT must be an existing absolute real directory'
[[ "$MAINTENANCE_WAIT_ATTEMPTS" =~ ^[1-9][0-9]*$ ]] || die 'MAINTENANCE_WAIT_ATTEMPTS must be a positive integer'
[[ "$MAINTENANCE_WAIT_INTERVAL_SECONDS" =~ ^[0-9]+$ ]] || die 'MAINTENANCE_WAIT_INTERVAL_SECONDS must be a non-negative integer'

for command_name in "$KUBECTL_BIN" "$HELM_BIN" "$CURL_BIN" "$SHA256_BIN" "$KUBERAY_BACKUP_BIN"; do
  command -v "$command_name" >/dev/null || die "missing command: $command_name"
done

current_context="$(kubectl config current-context)" || die 'cannot read current context'
[[ -n "$current_context" ]] || die 'current context is empty'
target_context="${KUBERAY_CONTEXT:-$current_context}"
echo "current context: ${current_context}; target context: ${target_context}" >&2
[[ "$target_context" =~ ^[A-Za-z0-9._/@:-]+$ ]] || die 'target context contains unsafe characters'
[[ "${CONFIRM_KUBE_CONTEXT:-}" == "$target_context" ]] || die "set CONFIRM_KUBE_CONTEXT=${target_context} to confirm the target cluster"

read -r expected_chart_digest expected_chart_name extra <"$KUBERAY_CHART_CHECKSUM_FILE" || die 'cannot read committed KubeRay chart checksum'
[[ -z "${extra:-}" && "$expected_chart_digest" =~ ^[0-9a-f]{64}$ && "$expected_chart_name" == kuberay-operator-1.6.2.tgz ]] || die 'committed KubeRay chart checksum file is invalid'

artifact_dir="$(mktemp -d)"
chart_path="${artifact_dir}/kuberay-operator-1.6.2.tgz"
command "$CURL_BIN" --fail --location --proto '=https' --tlsv1.2 --output "$chart_path" "$KUBERAY_CHART_URL" || die 'cannot download the fixed official KubeRay chart'
[[ "$(sha256_file "$chart_path")" == "$expected_chart_digest" ]] || die 'KubeRay chart checksum mismatch'
chart_metadata="$(helm show chart "$chart_path")" || die 'cannot inspect the downloaded KubeRay chart'
grep -Fxq 'name: kuberay-operator' <<<"$chart_metadata" || die 'downloaded chart name is not kuberay-operator'
grep -Fxq 'version: 1.6.2' <<<"$chart_metadata" || die 'downloaded chart version is not 1.6.2'

crd_dir="${artifact_dir}/crds"
mkdir -m 0700 -- "$crd_dir"
crd_files=(ray.io_rayclusters.yaml ray.io_rayjobs.yaml ray.io_rayservices.yaml ray.io_raycronjobs.yaml)
crd_names=(rayclusters.ray.io rayjobs.ray.io rayservices.ray.io raycronjobs.ray.io)
aggregate="${artifact_dir}/crds.aggregate"
: >"$aggregate"
for index in "${!crd_files[@]}"; do
  file="${crd_files[$index]}"
  name="${crd_names[$index]}"
  command "$CURL_BIN" --fail --location --proto '=https' --tlsv1.2 --output "${crd_dir}/${file}" "${KUBERAY_CRD_BASE_URL}/${file}" || die "cannot download fixed KubeRay CRD: $file"
  grep -Fxq 'apiVersion: apiextensions.k8s.io/v1' "${crd_dir}/${file}" || die "CRD is not apiextensions.k8s.io/v1: $file"
  grep -Fq "name: ${name}" "${crd_dir}/${file}" || die "CRD identity mismatch: $file"
  printf 'FILE %s\n' "$file" >>"$aggregate"
  command cat "${crd_dir}/${file}" >>"$aggregate"
done
[[ "$(sha256_file "$aggregate")" == "$EXPECTED_KUBERAY_CRD_SHA256" ]] || die 'KubeRay CRD aggregate checksum mismatch'
printf 'resources:\n' >"${crd_dir}/kustomization.yaml"
for file in "${crd_files[@]}"; do printf -- '- %s\n' "$file" >>"${crd_dir}/kustomization.yaml"; done

KUBERAY_CONTEXT="$target_context" "${script_dir}/preflight-upgrade.sh"
backup_parent="$(cd -- "$KUBERAY_BACKUP_PARENT" && pwd -P)"
backup_dir="$(KUBERAY_CONTEXT="$target_context" "$KUBERAY_BACKUP_BIN" "$backup_parent")" || die 'mandatory KubeRay backup failed'
[[ "$backup_dir" == "$backup_parent"/* && -d "$backup_dir" && ! -L "$backup_dir" ]] || die 'backup returned an unsafe path'
[[ -f "${backup_dir}/COMPLETE" && "$(cat "${backup_dir}/COMPLETE")" == COMPLETE ]] || die 'backup is incomplete'

verified_backup_files=0
verified_backup_names='|'
while read -r digest file extra; do
  [[ -z "${extra:-}" && "$digest" =~ ^[0-9a-f]{64}$ && "$file" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || die 'backup checksum manifest contains an unsafe entry'
  case "$file" in
    context.txt|crds.yaml|operator-deployment.yaml|operator-pod-images.txt|operator-helm-values.yaml|operator-helm-status.yaml|operator-helm-history.yaml|operator-helm-manifest.yaml|operator-chart-metadata.json|ray-resources.yaml|kueue-resources.yaml) ;;
    *) die "backup checksum manifest contains an unexpected file: $file" ;;
  esac
  [[ "$verified_backup_names" != *"|${file}|"* ]] || die "backup checksum manifest contains a duplicate file: $file"
  [[ -f "${backup_dir}/${file}" && "$(sha256_file "${backup_dir}/${file}")" == "$digest" ]] || die "backup checksum mismatch: $file"
  verified_backup_names="${verified_backup_names}${file}|"
  verified_backup_files=$((verified_backup_files + 1))
done <"${backup_dir}/checksums.sha256"
[[ "$verified_backup_files" == 11 ]] || die "backup checksum manifest has ${verified_backup_files} entries, expected 11"
for required_backup_file in context.txt crds.yaml operator-deployment.yaml operator-pod-images.txt operator-helm-values.yaml operator-helm-status.yaml operator-helm-history.yaml operator-helm-manifest.yaml operator-chart-metadata.json ray-resources.yaml kueue-resources.yaml; do
  [[ "$verified_backup_names" == *"|${required_backup_file}|"* ]] || die "backup checksum manifest is missing: $required_backup_file"
done
echo "backup path: ${backup_dir}" >&2

backend_replicas="$(kubectl get deployment "$PLATFORM_BACKEND_DEPLOYMENT" --context "$target_context" -n "$PLATFORM_NAMESPACE" -o jsonpath='{.spec.replicas}{"\n"}')" || die 'cannot record backend replica count'
[[ "$backend_replicas" =~ ^[0-9]+$ && "$backend_replicas" -gt 0 ]] || die 'backend must have a positive replica count before maintenance'
queue_state_file="${artifact_dir}/queue-state.tsv"
queue_rows="$(kubectl get clusterqueues.kueue.x-k8s.io --all-namespaces --context "$target_context" -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.stopPolicy}{"\n"}{end}')" || die 'cannot record ClusterQueue stopPolicy values'
while IFS=$'\t' read -r name policy; do
  [[ -n "$name" && "$name" =~ ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$ ]] || continue
  [[ -n "$policy" ]] || policy=__ABSENT__
  [[ "$policy" == __ABSENT__ || "$policy" == None ]] || die "ClusterQueue already has a non-admitting stopPolicy: ${name}=${policy}"
  printf '%s\t%s\n' "$name" "$policy" >>"$queue_state_file"
done <<<"$queue_rows"
[[ -s "$queue_state_file" ]] || die 'no ClusterQueue state was recorded'

maintenance_active=true
kubectl scale "deployment/${PLATFORM_BACKEND_DEPLOYMENT}" --replicas=0 --context "$target_context" -n "$PLATFORM_NAMESPACE" >/dev/null
wait_for_backend_replicas 0 || die 'backend maintenance gate did not reach zero Ready replicas'
queues_patched=true
while IFS=$'\t' read -r name _policy; do
  kubectl patch clusterqueue "$name" --type merge -p '{"spec":{"stopPolicy":"Hold"}}' --context "$target_context" >/dev/null
  kubectl wait --for=condition=Active=false "clusterqueue/${name}" --timeout=120s --context "$target_context" >/dev/null || die "ClusterQueue maintenance gate did not become inactive: $name"
done <"$queue_state_file"

PREFLIGHT_EXPECT_QUEUES_HELD=1 KUBERAY_CONTEXT="$target_context" "${script_dir}/preflight-upgrade.sh"
kubectl replace -k "$crd_dir" --context "$target_context"
helm upgrade "$KUBERAY_RELEASE" "$chart_path" \
  --namespace "$KUBERAY_NAMESPACE" \
  --kube-context "$target_context" \
  --skip-crds --reuse-values \
  --set-string "image.repository=${KUBERAY_OPERATOR_REPOSITORY}" \
  --set-string "image.tag=${KUBERAY_OPERATOR_TAG}" \
  --set replicas=2 \
  --atomic --wait --timeout 10m

export EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST CONFIRM_KUBERAY_OPERATOR_IMAGE_DIGEST
KUBERAY_CONTEXT="$target_context" "${script_dir}/verify.sh"
restore_maintenance || die 'upgrade succeeded but maintenance admission restoration failed'
maintenance_active=false
queues_patched=false
rm -rf -- "$artifact_dir"
artifact_dir=''
trap - EXIT INT TERM
echo "KubeRay ${KUBERAY_TARGET_VERSION} guarded upgrade completed and maintenance gates were restored" >&2
