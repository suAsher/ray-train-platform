#!/usr/bin/env bash
set -euo pipefail

readonly KUBECTL_BIN="${KUBECTL_BIN:-kubectl}"
readonly KUBERAY_NAMESPACE="${KUBERAY_NAMESPACE:-kuberay-system}"
readonly KUBERAY_RELEASE="${KUBERAY_RELEASE:-kuberay-operator}"
readonly KUBERAY_DEPLOYMENT="${KUBERAY_DEPLOYMENT:-kuberay-operator}"
readonly KUEUE_NAMESPACE="${KUEUE_NAMESPACE:-kueue-system}"
readonly KUEUE_DEPLOYMENT="${KUEUE_DEPLOYMENT:-kueue-controller-manager}"
readonly PREFLIGHT_EXPECT_QUEUES_HELD="${PREFLIGHT_EXPECT_QUEUES_HELD:-0}"

die() {
  echo "KubeRay preflight failed: $*" >&2
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

require_api_access() {
  local context="$1" resource allowed
  for resource in rayjobs.ray.io rayclusters.ray.io rayservices.ray.io workloads.kueue.x-k8s.io; do
    allowed="$(kubectl auth can-i list "$resource" --all-namespaces --context "$context")" || die "cannot check API access for $resource"
    [[ "$allowed" == yes ]] || die "API access denied for $resource"
  done
}

require_v1_served_storage() {
  local context="$1" crd="$2" versions
  versions="$(kubectl get crd "$crd" --context "$context" -o jsonpath='{range .spec.versions[*]}{.name}{" "}{.served}{" "}{.storage}{"\n"}{end}')" || die "cannot read CRD $crd"
  grep -Fxq 'v1 true true' <<<"$versions" || die "CRD $crd does not serve and store ray.io/v1"
}

require_two_ready_replicas() {
  local context="$1" namespace="$2" deployment="$3" description="$4"
  local desired ready available
  read -r desired ready available < <(kubectl get deployment "$deployment" --context "$context" -n "$namespace" -o jsonpath='{.spec.replicas}{" "}{.status.readyReplicas}{" "}{.status.availableReplicas}{"\n"}') || die "cannot read ${description} deployment"
  [[ "$desired" == 2 && "$ready" == 2 && "$available" == 2 ]] || die "${description} operator replicas are not ready: desired=${desired:-0} ready=${ready:-0} available=${available:-0}"
}

require_clusterqueue_gate_state() {
  local context="$1" rows name active stop_policy found=false
  rows="$(kubectl get clusterqueues.kueue.x-k8s.io --all-namespaces --context "$context" -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.conditions[?(@.type=="Active")].status}{" "}{.spec.stopPolicy}{"\n"}{end}')" || die 'cannot list Kueue ClusterQueues'
  while read -r name active stop_policy; do
    [[ -n "$name" ]] || continue
    found=true
    if [[ "$PREFLIGHT_EXPECT_QUEUES_HELD" == 1 ]]; then
      [[ "$stop_policy" == Hold && "$active" == False ]] || die "ClusterQueue maintenance gate is not effective: ${name} active=${active:-<missing>} stopPolicy=${stop_policy:-<missing>}"
    else
      [[ "$active" == True && "$stop_policy" != Hold && "$stop_policy" != HoldAndDrain ]] || die "ClusterQueue is not Active: ${name} status=${active:-<missing>} stopPolicy=${stop_policy:-<missing>}"
    fi
  done <<<"$rows"
  [[ "$found" == true ]] || die 'no Kueue ClusterQueue is configured'
}

require_no_active_kueue_workloads() {
  local context="$1" rows namespace name finished
  rows="$(kubectl get workloads.kueue.x-k8s.io --all-namespaces --context "$context" -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{" "}{.status.conditions[?(@.type=="Finished")].status}{"\n"}{end}')" || die 'cannot list Kueue Workloads'
  while read -r namespace name finished; do
    [[ -n "$namespace" ]] || continue
    [[ "$finished" == True ]] || die "non-terminal Kueue Workload exists: ${namespace}/${name} Finished=${finished:-<missing>}"
  done <<<"$rows"
}

require_no_nonterminal_rayjobs() {
  local context="$1" rows namespace name state deployment_state failed succeeded normalized normalized_deployment
  rows="$(kubectl get rayjobs.ray.io --all-namespaces --context "$context" -o jsonpath='{range .items[*]}{.metadata.namespace}{"|"}{.metadata.name}{"|"}{.status.jobStatus}{"|"}{.status.jobDeploymentStatus}{"|"}{.status.failed}{"|"}{.status.succeeded}{"\n"}{end}')" || die 'cannot list RayJobs'
  while IFS='|' read -r namespace name state deployment_state failed succeeded; do
    [[ -n "$namespace" ]] || continue
    normalized="$(printf '%s' "$state" | tr '[:lower:]' '[:upper:]')"
    normalized_deployment="$(printf '%s' "$deployment_state" | tr '[:lower:]' '[:upper:]')"
    case "$normalized" in
      SUCCEEDED|FAILED|STOPPED) continue ;;
    esac
    [[ -z "$normalized" ]] || die "non-terminal RayJob exists: ${namespace}/${name} status=${state} deploymentStatus=${deployment_state:-<missing>} failed=${failed:-0} succeeded=${succeeded:-0}"
    case "$normalized_deployment" in
      COMPLETE|FAILED) continue ;;
      *) die "non-terminal RayJob exists: ${namespace}/${name} status=${state:-<missing>} deploymentStatus=${deployment_state:-<missing>} failed=${failed:-0} succeeded=${succeeded:-0}" ;;
    esac
  done <<<"$rows"
}

require_no_active_clusters() {
  local context="$1" selector="$2" description="$3" rows namespace name state normalized
  if [[ -n "$selector" ]]; then
    rows="$(kubectl get rayclusters.ray.io --all-namespaces --context "$context" -l "$selector" -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{" "}{.status.state}{"\n"}{end}')" || die "cannot list ${description}"
  else
    rows="$(kubectl get rayclusters.ray.io --all-namespaces --context "$context" -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{" "}{.status.state}{"\n"}{end}')" || die "cannot list ${description}"
  fi
  while read -r namespace name state; do
    [[ -n "$namespace" ]] || continue
    normalized="$(printf '%s' "$state" | tr '[:lower:]' '[:upper:]')"
    case "$normalized" in
      FAILED|SUSPENDED) ;;
      *) die "active ${description} exists: ${namespace}/${name} state=${state:-<missing>}" ;;
    esac
  done <<<"$rows"
}

validate_name "$KUBERAY_NAMESPACE" KUBERAY_NAMESPACE
validate_name "$KUBERAY_RELEASE" KUBERAY_RELEASE
validate_name "$KUBERAY_DEPLOYMENT" KUBERAY_DEPLOYMENT
validate_name "$KUEUE_NAMESPACE" KUEUE_NAMESPACE
validate_name "$KUEUE_DEPLOYMENT" KUEUE_DEPLOYMENT
[[ "$PREFLIGHT_EXPECT_QUEUES_HELD" == 0 || "$PREFLIGHT_EXPECT_QUEUES_HELD" == 1 ]] || die 'PREFLIGHT_EXPECT_QUEUES_HELD must be 0 or 1'
target_context="$(confirm_context)"
require_api_access "$target_context"

for crd in rayjobs.ray.io rayclusters.ray.io rayservices.ray.io; do
  require_v1_served_storage "$target_context" "$crd"
done
for crd in clusterqueues.kueue.x-k8s.io localqueues.kueue.x-k8s.io; do
  kubectl get crd "$crd" --context "$target_context" >/dev/null || die "required Kueue CRD is absent: $crd"
done

require_two_ready_replicas "$target_context" "$KUBERAY_NAMESPACE" "$KUBERAY_DEPLOYMENT" KubeRay
require_two_ready_replicas "$target_context" "$KUEUE_NAMESPACE" "$KUEUE_DEPLOYMENT" Kueue
require_clusterqueue_gate_state "$target_context"
require_no_nonterminal_rayjobs "$target_context"
require_no_active_clusters "$target_context" 'ray.io/dev-workspace=true' 'debug workspace'
require_no_active_clusters "$target_context" '' RayCluster
require_no_active_kueue_workloads "$target_context"

echo "KubeRay upgrade preflight passed for context ${target_context}" >&2
