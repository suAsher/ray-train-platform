#!/usr/bin/env bash
set -euo pipefail

readonly NAMESPACE="mlflow-system"

for command in kubectl helm; do
  command -v "$command" >/dev/null || { echo "missing command: ${command}" >&2; exit 1; }
done

helm -n "$NAMESPACE" status mlflow >/dev/null
kubectl -n "$NAMESPACE" rollout status deployment/mlflow --timeout=5m
kubectl -n "$NAMESPACE" rollout status deployment/mlflow-ingest --timeout=5m
kubectl -n "$NAMESPACE" rollout status statefulset/mlflow-postgres --timeout=5m

service_type="$(kubectl -n "$NAMESPACE" get service mlflow -o jsonpath='{.spec.type}')"
[[ "$service_type" == "ClusterIP" ]] || { echo "MLflow service is not ClusterIP: ${service_type}" >&2; exit 1; }
[[ "$(kubectl -n "$NAMESPACE" get deployment mlflow -o jsonpath='{.status.availableReplicas}')" == "2" ]] || {
  echo "MLflow does not have two available replicas" >&2
  exit 1
}
[[ "$(kubectl -n "$NAMESPACE" get deployment mlflow-ingest -o jsonpath='{.status.availableReplicas}')" == "2" ]] || {
  echo "MLflow ingest gateway does not have two available replicas" >&2
  exit 1
}
[[ "$(kubectl -n "$NAMESPACE" get service mlflow-ingest -o jsonpath='{.spec.type}')" == "ClusterIP" ]]
kubectl -n "$NAMESPACE" get networkpolicy mlflow >/dev/null
kubectl -n "$NAMESPACE" get networkpolicy mlflow-ingest >/dev/null
kubectl -n "$NAMESPACE" get poddisruptionbudget mlflow >/dev/null
kubectl -n "$NAMESPACE" get servicemonitor mlflow >/dev/null
[[ "$(kubectl -n "$NAMESPACE" get service mlflow-postgres -o jsonpath='{.spec.type}')" == "ClusterIP" ]]
[[ "$(kubectl -n "$NAMESPACE" get pvc data-mlflow-postgres-0 -o jsonpath='{.status.phase}')" == "Bound" ]]
if kubectl -n "$NAMESPACE" get ingress -l app.kubernetes.io/name=mlflow -o name | grep -q .; then
  echo "MLflow must not have an external Ingress" >&2
  exit 1
fi
if kubectl -n "$NAMESPACE" get deployment mlflow -o yaml | grep -Fq 'claimName: mlflow-artifacts'; then
  echo "MLflow must use native TOS S3, not the node FSX mount" >&2
  exit 1
fi

health="$(kubectl get --raw '/api/v1/namespaces/mlflow-system/services/http:mlflow:5000/proxy/health')"
[[ "$health" == "OK" || "$health" == *'status'* ]] || { echo "unexpected MLflow health response: ${health}" >&2; exit 1; }
echo "MLflow production deployment verified"
