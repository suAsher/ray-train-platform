#!/usr/bin/env bash
set -euo pipefail

readonly KUBECTL_BIN="${KUBECTL_BIN:-kubectl}"
readonly HELM_BIN="${HELM_BIN:-helm}"
readonly KUBERAY_NAMESPACE="${KUBERAY_NAMESPACE:-kuberay-system}"
readonly KUBERAY_RELEASE="${KUBERAY_RELEASE:-kuberay-operator}"
readonly KUBERAY_DEPLOYMENT="${KUBERAY_DEPLOYMENT:-kuberay-operator}"
readonly KUBERAY_EXPECTED_CHART='kuberay-operator-1.6.2'
readonly KUBERAY_OPERATOR_REPOSITORY="${KUBERAY_OPERATOR_REPOSITORY:-quay.io/kuberay/operator}"
readonly KUBERAY_EXPECTED_IMAGE="${KUBERAY_OPERATOR_REPOSITORY}:v1.6.2"
readonly EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST="${EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST:-}"

die() {
  echo "KubeRay verification failed: $*" >&2
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

validate_name "$KUBERAY_NAMESPACE" KUBERAY_NAMESPACE
validate_name "$KUBERAY_RELEASE" KUBERAY_RELEASE
validate_name "$KUBERAY_DEPLOYMENT" KUBERAY_DEPLOYMENT
[[ "$KUBERAY_OPERATOR_REPOSITORY" =~ ^[a-z0-9][a-z0-9.-]*(:[0-9]+)?/[a-z0-9][a-z0-9._/-]*[a-z0-9]$ && "$KUBERAY_OPERATOR_REPOSITORY" != *..* && "$KUBERAY_OPERATOR_REPOSITORY" != *//* ]] || die 'KUBERAY_OPERATOR_REPOSITORY must be a safe registry repository without a tag or digest'
[[ "$EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST" =~ ^[0-9a-f]{64}$ ]] || die 'EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST must be 64 lowercase hexadecimal characters'
[[ "${CONFIRM_KUBERAY_OPERATOR_IMAGE_DIGEST:-}" == "$EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST" ]] || die 'CONFIRM_KUBERAY_OPERATOR_IMAGE_DIGEST must exactly match the expected digest'
target_context="$(confirm_context)"
for crd in rayjobs.ray.io rayclusters.ray.io rayservices.ray.io raycronjobs.ray.io; do
  versions="$(kubectl get crd "$crd" --context "$target_context" -o jsonpath='{range .spec.versions[*]}{.name}{" "}{.served}{" "}{.storage}{"\n"}{end}')" || die "cannot read CRD $crd"
  grep -Fxq 'v1 true true' <<<"$versions" || die "CRD $crd served/storage contract does not include v1 true true"
done

kubectl rollout status deployment/"$KUBERAY_DEPLOYMENT" --context "$target_context" -n "$KUBERAY_NAMESPACE" --timeout=120s
read -r desired ready available < <(kubectl get deployment "$KUBERAY_DEPLOYMENT" --context "$target_context" -n "$KUBERAY_NAMESPACE" -o jsonpath='{.spec.replicas}{" "}{.status.readyReplicas}{" "}{.status.availableReplicas}{"\n"}') || die 'cannot read KubeRay operator replicas'
[[ "$desired" == 2 && "$ready" == 2 && "$available" == 2 ]] || die "KubeRay operator is not 2/2 Ready: desired=${desired:-0} readyReplicas=${ready:-0} available=${available:-0}"

release_metadata="$(helm list --filter "^${KUBERAY_RELEASE}$" --namespace "$KUBERAY_NAMESPACE" --kube-context "$target_context" -o json)" || die 'cannot read installed KubeRay Helm release metadata'
grep -Fq '"name":"'"${KUBERAY_RELEASE}"'"' <<<"$release_metadata" || die 'KubeRay Helm release name does not match the requested release'
grep -Fq '"chart":"'"${KUBERAY_EXPECTED_CHART}"'"' <<<"$release_metadata" || die "installed Helm chart is not ${KUBERAY_EXPECTED_CHART}"

deployment_images="$(kubectl get deployment "$KUBERAY_DEPLOYMENT" --context "$target_context" -n "$KUBERAY_NAMESPACE" -o jsonpath='{range .spec.template.spec.containers[*]}{.name}{" "}{.image}{"\n"}{end}')" || die 'cannot read KubeRay operator Deployment images'
deployment_image_found=false
while read -r container image; do
  [[ "$container" == kuberay-operator ]] || continue
  deployment_image_found=true
  [[ "$image" == "$KUBERAY_EXPECTED_IMAGE" ]] || die "KubeRay Deployment image is not ${KUBERAY_EXPECTED_IMAGE}"
done <<<"$deployment_images"
[[ "$deployment_image_found" == true ]] || die 'KubeRay operator container is absent from the Deployment'

pod_images="$(kubectl get pods --context "$target_context" -n "$KUBERAY_NAMESPACE" -l "app.kubernetes.io/instance=${KUBERAY_RELEASE}" -o jsonpath='{range .items[*]}{.metadata.name}{" "}{range .status.containerStatuses[*]}{.name}{" "}{.image}{" "}{.imageID}{"\n"}{end}{end}')" || die 'cannot read running KubeRay operator image IDs'
operator_pods=0
while read -r pod container image image_id; do
  [[ "$container" == kuberay-operator ]] || continue
  operator_pods=$((operator_pods + 1))
  [[ "$image" == "$KUBERAY_EXPECTED_IMAGE" ]] || die "KubeRay Pod ${pod} image is not ${KUBERAY_EXPECTED_IMAGE}"
  [[ "$image_id" == *@sha256:"$EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST" ]] || die "KubeRay Pod ${pod} imageID digest does not match the confirmed digest"
done <<<"$pod_images"
[[ "$operator_pods" == 2 ]] || die "expected two running KubeRay operator images, found ${operator_pods}"

kubectl get --raw /apis/ray.io/v1 --context "$target_context" >/dev/null || die 'ray.io/v1 API discovery failed'
kubectl api-resources --api-group=ray.io --context "$target_context" >/dev/null || die 'ray.io API resources are unavailable'
release_manifest="$(helm get manifest "$KUBERAY_RELEASE" --namespace "$KUBERAY_NAMESPACE" --kube-context "$target_context")" || die 'cannot read installed KubeRay Helm manifest'
declares_validating_webhook=false
declares_mutating_webhook=false
grep -Fxq 'kind: ValidatingWebhookConfiguration' <<<"$release_manifest" && declares_validating_webhook=true
grep -Fxq 'kind: MutatingWebhookConfiguration' <<<"$release_manifest" && declares_mutating_webhook=true
if [[ "$declares_validating_webhook" == true ]]; then
  validating_webhook="$(kubectl get validatingwebhookconfigurations.admissionregistration.k8s.io --context "$target_context" -l "app.kubernetes.io/instance=${KUBERAY_RELEASE}" -o name)" || die 'cannot read KubeRay validating webhook'
  [[ -n "$validating_webhook" ]] || die 'KubeRay Helm manifest declares a validating webhook but no matching configuration is installed'
fi
if [[ "$declares_mutating_webhook" == true ]]; then
  mutating_webhook="$(kubectl get mutatingwebhookconfigurations.admissionregistration.k8s.io --context "$target_context" -l "app.kubernetes.io/instance=${KUBERAY_RELEASE}" -o name)" || die 'cannot read KubeRay mutating webhook'
  [[ -n "$mutating_webhook" ]] || die 'KubeRay Helm manifest declares a mutating webhook but no matching configuration is installed'
fi
if [[ "$declares_validating_webhook" != true && "$declares_mutating_webhook" != true ]]; then
  echo 'KubeRay Helm manifest does not declare an admission webhook; webhook verification is not applicable' >&2
fi

allowed="$(kubectl auth can-i list rayjobs.ray.io --all-namespaces --context "$target_context")" || die 'cannot check Ray resource API access'
[[ "$allowed" == yes ]] || die 'Ray resource API access is denied'
kubectl get rayjobs.ray.io,rayclusters.ray.io,rayservices.ray.io,raycronjobs.ray.io --all-namespaces --context "$target_context" -o name >/dev/null || die 'existing Ray resources are not readable'
kubectl get clusterqueues.kueue.x-k8s.io,resourceflavors.kueue.x-k8s.io,localqueues.kueue.x-k8s.io,workloads.kueue.x-k8s.io --all-namespaces --context "$target_context" -o name >/dev/null || die 'existing Kueue resources are not readable'

echo "KubeRay 1.6.2 verification passed for context ${target_context}" >&2
