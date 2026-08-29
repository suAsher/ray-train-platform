#!/usr/bin/env bash
set -euo pipefail
umask 077

readonly KUBECTL_BIN="${KUBECTL_BIN:-kubectl}"
readonly HELM_BIN="${HELM_BIN:-helm}"
readonly SHA256_BIN="${SHA256_BIN:-shasum}"
readonly KUBERAY_NAMESPACE="${KUBERAY_NAMESPACE:-kuberay-system}"
readonly KUBERAY_RELEASE="${KUBERAY_RELEASE:-kuberay-operator}"
readonly KUBERAY_DEPLOYMENT="${KUBERAY_DEPLOYMENT:-kuberay-operator}"

die() {
  echo "KubeRay backup failed: $*" >&2
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

confirm_context() {
  command -v "$KUBECTL_BIN" >/dev/null || die "missing command: $KUBECTL_BIN"
  command -v "$HELM_BIN" >/dev/null || die "missing command: $HELM_BIN"
  command -v "$SHA256_BIN" >/dev/null || die "missing command: $SHA256_BIN"
  local current_context target_context
  current_context="$(kubectl config current-context)" || die 'cannot read current context'
  [[ -n "$current_context" ]] || die 'current context is empty'
  target_context="${KUBERAY_CONTEXT:-$current_context}"
  echo "current context: ${current_context}; target context: ${target_context}" >&2
  [[ "$target_context" =~ ^[A-Za-z0-9._/@:-]+$ ]] || die 'target context contains unsafe characters'
  [[ "${CONFIRM_KUBE_CONTEXT:-}" == "$target_context" ]] || die "set CONFIRM_KUBE_CONTEXT=${target_context} to confirm the target cluster"
  kubectl version --request-timeout=15s --context "$target_context" >/dev/null || die 'Kubernetes API is unavailable'
  printf '%s\n' "$target_context"
}

[[ "$#" == 1 ]] || die 'usage: backup.sh /absolute/existing/backup-parent'
validate_name "$KUBERAY_NAMESPACE" KUBERAY_NAMESPACE
validate_name "$KUBERAY_RELEASE" KUBERAY_RELEASE
validate_name "$KUBERAY_DEPLOYMENT" KUBERAY_DEPLOYMENT
backup_parent="$1"
[[ "$backup_parent" == /* && "$backup_parent" != / && "$backup_parent" != *$'\n'* && "$backup_parent" != *'/../'* && "$backup_parent" != */.. ]] || die 'backup target must be an absolute safe parent path'
[[ -d "$backup_parent" && ! -L "$backup_parent" ]] || die 'backup parent must be an existing real directory'
canonical_parent="$(cd -- "$backup_parent" && pwd -P)"
target_context="$(confirm_context)"
backup_target="$(mktemp -d "${canonical_parent}/kuberay-1.6.2-$(date -u +%Y%m%dT%H%M%SZ).XXXXXXXX")"

printf 'created_at_utc=%s\ncurrent_context=%s\ntarget_context=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(kubectl config current-context)" "$target_context" >"${backup_target}/context.txt"

crd_names="$(kubectl get crd --context "$target_context" -o jsonpath='{range .items[?(@.spec.group=="ray.io")]}{.metadata.name}{"\n"}{end}')" || die 'cannot list installed KubeRay CRDs'
crds=()
while read -r crd; do
  [[ -n "$crd" ]] || continue
  [[ "$crd" =~ ^[a-z0-9-]+\.ray\.io$ ]] || die "unsafe KubeRay CRD name: $crd"
  crds+=("$crd")
done <<<"$crd_names"
[[ "${#crds[@]}" -gt 0 ]] || die 'no installed KubeRay CRDs found'

kubectl get crd "${crds[@]}" --context "$target_context" -o yaml >"${backup_target}/crds.yaml"
kubectl get deployment "$KUBERAY_DEPLOYMENT" --context "$target_context" -n "$KUBERAY_NAMESPACE" -o yaml >"${backup_target}/operator-deployment.yaml"
kubectl get pods --context "$target_context" -n "$KUBERAY_NAMESPACE" -l "app.kubernetes.io/instance=${KUBERAY_RELEASE}" -o jsonpath='{range .items[*]}{.metadata.name}{" "}{range .status.containerStatuses[*]}{.name}{" "}{.image}{" "}{.imageID}{"\n"}{end}{end}' >"${backup_target}/operator-pod-images.txt"
helm get values "$KUBERAY_RELEASE" --namespace "$KUBERAY_NAMESPACE" --kube-context "$target_context" --all >"${backup_target}/operator-helm-values.yaml"
helm status "$KUBERAY_RELEASE" --namespace "$KUBERAY_NAMESPACE" --kube-context "$target_context" -o yaml >"${backup_target}/operator-helm-status.yaml"
helm history "$KUBERAY_RELEASE" --namespace "$KUBERAY_NAMESPACE" --kube-context "$target_context" -o yaml >"${backup_target}/operator-helm-history.yaml"
helm get manifest "$KUBERAY_RELEASE" --namespace "$KUBERAY_NAMESPACE" --kube-context "$target_context" >"${backup_target}/operator-helm-manifest.yaml"
helm list --filter "^${KUBERAY_RELEASE}$" --namespace "$KUBERAY_NAMESPACE" --kube-context "$target_context" -o json >"${backup_target}/operator-chart-metadata.json"
ray_resource_types="$(IFS=,; printf '%s' "${crds[*]}")"
kubectl get "$ray_resource_types" --all-namespaces --context "$target_context" -o yaml >"${backup_target}/ray-resources.yaml"
kubectl get clusterqueues.kueue.x-k8s.io,resourceflavors.kueue.x-k8s.io,localqueues.kueue.x-k8s.io,workloads.kueue.x-k8s.io --all-namespaces --context "$target_context" -o yaml >"${backup_target}/kueue-resources.yaml"

artifacts=(
  context.txt
  crds.yaml
  operator-deployment.yaml
  operator-pod-images.txt
  operator-helm-values.yaml
  operator-helm-status.yaml
  operator-helm-history.yaml
  operator-helm-manifest.yaml
  operator-chart-metadata.json
  ray-resources.yaml
  kueue-resources.yaml
)
checksum_tmp="${backup_target}/.checksums.sha256.tmp"
for artifact in "${artifacts[@]}"; do
  [[ -s "${backup_target}/${artifact}" ]] || die "backup artifact is empty: $artifact"
  printf '%s  %s\n' "$(sha256_file "${backup_target}/${artifact}")" "$artifact" >>"$checksum_tmp"
done
mv -- "$checksum_tmp" "${backup_target}/checksums.sha256"
printf 'COMPLETE\n' >"${backup_target}/.COMPLETE.tmp"
mv -- "${backup_target}/.COMPLETE.tmp" "${backup_target}/COMPLETE"

echo "KubeRay backup written to ${backup_target}" >&2
printf '%s\n' "$backup_target"
