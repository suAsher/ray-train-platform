#!/usr/bin/env bash
set -euo pipefail
umask 077

readonly KUBECTL_BIN="${KUBECTL_BIN:-kubectl}"
readonly HELM_BIN="${HELM_BIN:-helm}"
readonly KUBERAY_NAMESPACE="${KUBERAY_NAMESPACE:-kuberay-system}"
readonly KUBERAY_RELEASE="${KUBERAY_RELEASE:-kuberay-operator}"

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

confirm_context() {
  command -v "$KUBECTL_BIN" >/dev/null || die "missing command: $KUBECTL_BIN"
  command -v "$HELM_BIN" >/dev/null || die "missing command: $HELM_BIN"
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

[[ "$#" == 1 ]] || die 'usage: backup.sh /absolute/new/backup-directory'
validate_name "$KUBERAY_NAMESPACE" KUBERAY_NAMESPACE
validate_name "$KUBERAY_RELEASE" KUBERAY_RELEASE
backup_target="$1"
[[ "$backup_target" == /* ]] || die 'backup target must be an absolute path'
[[ "$backup_target" != / && "$backup_target" != *$'\n'* && "$backup_target" != *'/../'* && "$backup_target" != */.. ]] || die 'backup target path is unsafe'
[[ ! -e "$backup_target" && ! -L "$backup_target" ]] || die 'backup target already exists'
backup_parent="$(dirname -- "$backup_target")"
backup_name="$(basename -- "$backup_target")"
[[ "$backup_name" != . && "$backup_name" != .. && -n "$backup_name" ]] || die 'backup target name is unsafe'
[[ "$backup_name" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || die 'backup target name contains unsafe characters'
[[ -d "$backup_parent" && ! -L "$backup_parent" ]] || die 'backup target parent must be an existing real directory'
canonical_parent="$(cd -- "$backup_parent" && pwd -P)"
backup_target="${canonical_parent}/${backup_name}"

target_context="$(confirm_context)"
mkdir -m 0700 -- "$backup_target"
printf 'created_at_utc=%s\ncurrent_context=%s\ntarget_context=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(kubectl config current-context)" "$target_context" >"${backup_target}/context.txt"

kubectl get crd rayjobs.ray.io rayclusters.ray.io rayservices.ray.io --context "$target_context" -o yaml >"${backup_target}/crds.yaml"
kubectl get deployment "$KUBERAY_RELEASE" --context "$target_context" -n "$KUBERAY_NAMESPACE" -o yaml >"${backup_target}/operator-deployment.yaml"
helm get values "$KUBERAY_RELEASE" --namespace "$KUBERAY_NAMESPACE" --kube-context "$target_context" --all >"${backup_target}/operator-helm-values.yaml"
kubectl get rayjobs.ray.io,rayclusters.ray.io,rayservices.ray.io --all-namespaces --context "$target_context" -o yaml >"${backup_target}/ray-resources.yaml"
kubectl get clusterqueues.kueue.x-k8s.io,resourceflavors.kueue.x-k8s.io,localqueues.kueue.x-k8s.io,workloads.kueue.x-k8s.io --all-namespaces --context "$target_context" -o yaml >"${backup_target}/kueue-resources.yaml"

for artifact in context.txt crds.yaml operator-deployment.yaml operator-helm-values.yaml ray-resources.yaml kueue-resources.yaml; do
  [[ -s "${backup_target}/${artifact}" ]] || die "backup artifact is empty: $artifact"
done
echo "KubeRay backup written to ${backup_target}" >&2
