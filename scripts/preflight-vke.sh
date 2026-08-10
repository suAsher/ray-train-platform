#!/usr/bin/env bash
set -Eeuo pipefail

NAMESPACE="${NAMESPACE:-ray-train-platform}"
KEYCLOAK_ISSUER="${KEYCLOAK_ISSUER:-}"
IDC_PVC="${IDC_PVC:-idc-training-rwx}"
EXPECTED_GPUS="${EXPECTED_GPUS:-24}"
KUEUE_QUEUE="${KUEUE_QUEUE:-cluster-gpu-queue}"
HELM_VALUES_FILE="${HELM_VALUES_FILE:-}"

fail() { printf 'ERROR: %s\n' "$1" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "missing command: $1"; }

need kubectl
need helm
need jq
need curl

kubectl cluster-info --request-timeout=10s >/dev/null || fail "cannot reach the selected Kubernetes cluster"
kubectl get namespace "$NAMESPACE" >/dev/null || fail "namespace does not exist: $NAMESPACE"

for resource in rayjobs.ray.io rayclusters.ray.io; do
  kubectl get crd "$resource" >/dev/null || fail "missing CRD: $resource"
done
kubectl api-resources --api-group=kueue.x-k8s.io | grep -q workloads || fail "Kueue Workload API is not available"
kubectl get clusterqueue "$KUEUE_QUEUE" >/dev/null || fail "missing ClusterQueue: $KUEUE_QUEUE"

gpu_total="$(kubectl get nodes -o json | jq '[.items[].status.allocatable["nvidia.com/gpu"] // "0" | tonumber] | add // 0')"
[[ "$gpu_total" -ge "$EXPECTED_GPUS" ]] || fail "allocatable GPU total $gpu_total is below expected $EXPECTED_GPUS"

if [[ -n "$KEYCLOAK_ISSUER" ]]; then
  curl --fail --silent --show-error --max-time 10 "$KEYCLOAK_ISSUER/.well-known/openid-configuration" | jq -e '.issuer and .jwks_uri' >/dev/null || fail "Keycloak OIDC discovery failed"
fi

kubectl get pvc "$IDC_PVC" -n "$NAMESPACE" >/dev/null || printf 'WARNING: PVC %s is not in the platform namespace; tenant namespaces must each provide the configured claim.\n' "$IDC_PVC"
if [[ -n "$HELM_VALUES_FILE" ]]; then
  helm lint ./helm/ray-train-platform --values "$HELM_VALUES_FILE" >/dev/null || fail "Helm lint failed for values file: $HELM_VALUES_FILE"
else
  printf 'WARNING: skipped Helm lint; set HELM_VALUES_FILE to the production values file for chart validation.\n'
fi
printf 'Preflight passed: context=%s namespace=%s allocatable_gpus=%s\n' "$(kubectl config current-context)" "$NAMESPACE" "$gpu_total"
