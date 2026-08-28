#!/usr/bin/env bash
set -euo pipefail

readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly KUBECTL_BIN="${KUBECTL_BIN:-kubectl}"
readonly HELM_BIN="${HELM_BIN:-helm}"
readonly KUBERAY_TARGET_VERSION="1.6.2"
readonly KUBERAY_NAMESPACE="${KUBERAY_NAMESPACE:-kuberay-system}"
readonly KUBERAY_RELEASE="${KUBERAY_RELEASE:-kuberay-operator}"
readonly KUBERAY_CHART_URL="${KUBERAY_CHART_URL:-https://ray-project.github.io/kuberay-helm/}"
readonly KUBERAY_CRD_KUSTOMIZE_URL="${KUBERAY_CRD_KUSTOMIZE_URL:-https://github.com/ray-project/kuberay/ray-operator/config/crd?ref=v1.6.2&timeout=90s}"
readonly KUBERAY_HELM_REPO_NAME='kuberay-guarded-upgrade'
readonly KUBERAY_CRD_URL_PATTERN='^https://[A-Za-z0-9._~:/-]+\?ref=v1\.6\.2(&timeout=[1-9][0-9]*s)?$'

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

command -v "$KUBECTL_BIN" >/dev/null || die "missing command: $KUBECTL_BIN"
command -v "$HELM_BIN" >/dev/null || die "missing command: $HELM_BIN"

current_context="$(kubectl config current-context)" || die 'cannot read current context'
target_context="${KUBERAY_CONTEXT:-$current_context}"
echo "current context: ${current_context}; target context: ${target_context}" >&2
[[ "${CONFIRM_KUBE_CONTEXT:-}" == "$target_context" ]] || die "set CONFIRM_KUBE_CONTEXT=${target_context} to confirm the target cluster"
KUBERAY_CONTEXT="$target_context" "${script_dir}/preflight-upgrade.sh"
[[ "${CONFIRM_KUBERAY_UPGRADE:-0}" == 1 ]] || die 'set CONFIRM_KUBERAY_UPGRADE=1 after reviewing preflight and backup output'
[[ "$KUBERAY_CHART_URL" == https://* ]] || die 'KUBERAY_CHART_URL must use https://'
[[ "$KUBERAY_CRD_KUSTOMIZE_URL" =~ $KUBERAY_CRD_URL_PATTERN ]] || die 'KUBERAY_CRD_KUSTOMIZE_URL must be an HTTPS source pinned exactly to ref=v1.6.2'

# CRDs are replaced first; no Ray runtime image or tenant resource is mutated.
kubectl replace -k "$KUBERAY_CRD_KUSTOMIZE_URL" --context "$target_context"
helm repo add "$KUBERAY_HELM_REPO_NAME" "$KUBERAY_CHART_URL" --force-update
helm repo update "$KUBERAY_HELM_REPO_NAME"
helm upgrade "${KUBERAY_RELEASE}" "${KUBERAY_HELM_REPO_NAME}/kuberay-operator" \
  --namespace "$KUBERAY_NAMESPACE" \
  --kube-context "$target_context" \
  --version "$KUBERAY_TARGET_VERSION" \
  --skip-crds --reuse-values \
  --atomic --wait --timeout 10m

KUBERAY_CONTEXT="$target_context" "${script_dir}/verify.sh"
