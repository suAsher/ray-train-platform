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

services="$(kubectl -n "$NAMESPACE" get services -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.type}{"\n"}{end}')"
while IFS=$'\t' read -r service_name service_type; do
  [[ -n "$service_name" ]] || continue
  [[ "$service_type" == "ClusterIP" ]] || {
    echo "service ${service_name} in ${NAMESPACE} is not ClusterIP: ${service_type}" >&2
    exit 1
  }
done <<<"$services"

[[ "$(kubectl -n "$NAMESPACE" get deployment mlflow -o jsonpath='{.spec.replicas}')" == "2" ]] || {
  echo "MLflow deployment is not configured for two replicas" >&2
  exit 1
}
[[ "$(kubectl -n "$NAMESPACE" get deployment mlflow -o jsonpath='{.status.availableReplicas}')" == "2" ]] || {
  echo "MLflow does not have two available replicas" >&2
  exit 1
}
[[ "$(kubectl -n "$NAMESPACE" get deployment mlflow-ingest -o jsonpath='{.status.availableReplicas}')" == "2" ]] || {
  echo "MLflow ingest gateway does not have two available replicas" >&2
  exit 1
}
[[ "$(kubectl -n "$NAMESPACE" get pvc mlflow-artifacts -o jsonpath='{.status.phase}')" == "Bound" ]] || {
  echo "MLflow artifact PVC is not Bound" >&2
  exit 1
}
mlflow_command="$(kubectl -n "$NAMESPACE" get deployment mlflow -o jsonpath='{.spec.template.spec.containers[?(@.name=="mlflow")].command}')"
mlflow_args="$(kubectl -n "$NAMESPACE" get deployment mlflow -o jsonpath='{.spec.template.spec.containers[?(@.name=="mlflow")].args}')"
grep -Fq -- '--static-prefix=/mlflow' <<<"${mlflow_command} ${mlflow_args}" || {
  echo 'MLflow deployment is missing --static-prefix=/mlflow' >&2
  exit 1
}
artifact_claim="$(kubectl -n "$NAMESPACE" get deployment mlflow -o jsonpath='{.spec.template.spec.volumes[?(@.name=="mlflow-artifacts")].persistentVolumeClaim.claimName}')"
[[ "$artifact_claim" == "mlflow-artifacts" ]] || {
  echo "MLflow deployment does not reference the artifact PVC: ${artifact_claim}" >&2
  exit 1
}
artifact_mount="$(kubectl -n "$NAMESPACE" get deployment mlflow -o jsonpath='{.spec.template.spec.containers[?(@.name=="mlflow")].volumeMounts[?(@.name=="mlflow-artifacts")].mountPath}')"
[[ "$artifact_mount" == "/mlflow-artifacts" ]] || {
  echo "MLflow artifact PVC is not mounted at /mlflow-artifacts: ${artifact_mount}" >&2
  exit 1
}
artifact_destination="$(kubectl -n "$NAMESPACE" get deployment mlflow -o jsonpath='{.spec.template.spec.containers[?(@.name=="mlflow")].env[?(@.name=="MLFLOW_ARTIFACTS_DESTINATION")].value}')"
[[ "$artifact_destination" == "file:///mlflow-artifacts" ]] || {
  echo "unexpected MLflow artifact root: ${artifact_destination}" >&2
  exit 1
}
mlflow_deployment="$(kubectl -n "$NAMESPACE" get deployment mlflow -o yaml)"
if grep -Eq 'AWS_|TOS_|MLFLOW_(S3|BOTO)|tos-credentials|mlflow-aws-config|/etc/mlflow/aws' <<<"$mlflow_deployment"; then
  echo 'MLflow Pod still contains AWS/TOS credentials or configuration' >&2
  exit 1
fi
if kubectl -n "$NAMESPACE" get secret tos-credentials >/dev/null 2>&1; then
  echo 'shared tos-credentials Secret still exists in mlflow-system' >&2
  exit 1
fi
if kubectl -n "$NAMESPACE" get configmap mlflow-aws-config >/dev/null 2>&1; then
  echo 'legacy MLflow AWS config still exists in mlflow-system' >&2
  exit 1
fi
if kubectl -n "$NAMESPACE" get networkpolicy mlflow-storage-migration >/dev/null 2>&1; then
  echo 'temporary MLflow storage migration egress is still enabled' >&2
  exit 1
fi

kubectl -n "$NAMESPACE" get networkpolicy mlflow >/dev/null
kubectl -n "$NAMESPACE" get networkpolicy mlflow-ingest >/dev/null
kubectl -n "$NAMESPACE" get poddisruptionbudget mlflow >/dev/null
kubectl -n "$NAMESPACE" get servicemonitor mlflow >/dev/null
[[ "$(kubectl -n "$NAMESPACE" get pvc data-mlflow-postgres-0 -o jsonpath='{.status.phase}')" == "Bound" ]]

health="$(kubectl get --raw '/api/v1/namespaces/mlflow-system/services/http:mlflow:5000/proxy/mlflow/health')"
[[ "$health" == "OK" || "$health" == *'status'* ]] || { echo "unexpected MLflow health response: ${health}" >&2; exit 1; }
echo "MLflow production deployment verified"
