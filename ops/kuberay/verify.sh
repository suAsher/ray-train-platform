#!/usr/bin/env bash
set -euo pipefail

readonly KUBECTL_BIN="${KUBECTL_BIN:-kubectl}"
readonly KUBERAY_NAMESPACE="${KUBERAY_NAMESPACE:-kuberay-system}"
readonly KUBERAY_RELEASE="${KUBERAY_RELEASE:-kuberay-operator}"

die() {
  echo "KubeRay verification failed: $*" >&2
  exit 1
}

kubectl() {
  command "$KUBECTL_BIN" "$@"
}

validate_name() {
  [[ "$1" =~ ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$ ]] || die "unsafe ${2}: $1"
}

confirm_context() {
  command -v "$KUBECTL_BIN" >/dev/null || die "missing command: $KUBECTL_BIN"
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

validate_name "$KUBERAY_NAMESPACE" KUBERAY_NAMESPACE
validate_name "$KUBERAY_RELEASE" KUBERAY_RELEASE
target_context="$(confirm_context)"
for crd in rayjobs.ray.io rayclusters.ray.io rayservices.ray.io; do
  versions="$(kubectl get crd "$crd" --context "$target_context" -o jsonpath='{range .spec.versions[*]}{.name}{" "}{.served}{" "}{.storage}{"\n"}{end}')" || die "cannot read CRD $crd"
  grep -Fxq 'v1 true true' <<<"$versions" || die "CRD $crd served/storage contract does not include v1 true true"
done

kubectl rollout status deployment/"$KUBERAY_RELEASE" --context "$target_context" -n "$KUBERAY_NAMESPACE" --timeout=120s
read -r desired ready available < <(kubectl get deployment "$KUBERAY_RELEASE" --context "$target_context" -n "$KUBERAY_NAMESPACE" -o jsonpath='{.spec.replicas}{" "}{.status.readyReplicas}{" "}{.status.availableReplicas}{"\n"}') || die 'cannot read KubeRay operator replicas'
[[ "$desired" == 2 && "$ready" == 2 && "$available" == 2 ]] || die "KubeRay operator is not 2/2 Ready: desired=${desired:-0} readyReplicas=${ready:-0} available=${available:-0}"

kubectl get --raw /apis/ray.io/v1 --context "$target_context" >/dev/null || die 'ray.io/v1 API discovery failed'
kubectl api-resources --api-group=ray.io --context "$target_context" >/dev/null || die 'ray.io API resources are unavailable'
validating_webhook="$(kubectl get validatingwebhookconfigurations.admissionregistration.k8s.io --context "$target_context" -l "app.kubernetes.io/instance=${KUBERAY_RELEASE}" -o name)" || die 'cannot read KubeRay validating webhook'
mutating_webhook="$(kubectl get mutatingwebhookconfigurations.admissionregistration.k8s.io --context "$target_context" -l "app.kubernetes.io/instance=${KUBERAY_RELEASE}" -o name)" || die 'cannot read KubeRay mutating webhook'
[[ -n "${validating_webhook}${mutating_webhook}" ]] || die 'no KubeRay webhook configuration is installed'

allowed="$(kubectl auth can-i list rayjobs.ray.io --all-namespaces --context "$target_context")" || die 'cannot check Ray resource API access'
[[ "$allowed" == yes ]] || die 'Ray resource API access is denied'
kubectl get rayjobs.ray.io,rayclusters.ray.io,rayservices.ray.io --all-namespaces --context "$target_context" -o name >/dev/null || die 'existing Ray resources are not readable'
kubectl get clusterqueues.kueue.x-k8s.io,resourceflavors.kueue.x-k8s.io,localqueues.kueue.x-k8s.io,workloads.kueue.x-k8s.io --all-namespaces --context "$target_context" -o name >/dev/null || die 'existing Kueue resources are not readable'

echo "KubeRay 1.6.2 verification passed for context ${target_context}" >&2
