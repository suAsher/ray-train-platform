#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly VALUES="${ROOT_DIR}/ops/mlflow/values-vke.yaml"
readonly NAMESPACE="${ROOT_DIR}/ops/mlflow/00-namespace.yaml"
readonly DATABASE="${ROOT_DIR}/ops/mlflow/10-database.yaml"
readonly POLICY="${ROOT_DIR}/ops/mlflow/30-policy.yaml"
readonly SMOKE="${ROOT_DIR}/ops/mlflow/40-smoke.yaml"
readonly VERIFY="${ROOT_DIR}/ops/mlflow/verify.sh"
readonly VENDORED_CHART="${ROOT_DIR}/helm/vendor/mlflow-0.1.0.tgz"
readonly VENDORED_DEPLOYMENT="mlflow/templates/deployment.yaml"

vendored_deployment="$(tar -xOf "$VENDORED_CHART" "$VENDORED_DEPLOYMENT")"
grep -Fq '          command:' <<<"$vendored_deployment" || {
  echo 'vendored MLflow chart must render server flags in container.command' >&2
  exit 1
}
grep -Fq -- '- --static-prefix={{ .Values.server.staticPrefix }}' <<<"$vendored_deployment" || {
  echo 'vendored MLflow chart must render server.staticPrefix in container.command' >&2
  exit 1
}

grep -Fq 'replicaCount: 2' "$VALUES"
grep -Fq '  staticPrefix: /mlflow' "$VALUES" || {
  echo 'MLflow must be served under server.staticPrefix /mlflow' >&2
  exit 1
}
grep -Fq 'type: ClusterIP' "$VALUES"
grep -A1 '^ingress:$' "$VALUES" | grep -Fq '  enabled: false'
grep -Fq '@sha256:' "$VALUES"
grep -Fq 'name: mlflow-database' "$VALUES"
grep -Fq 'automountServiceAccountToken: false' "$VALUES"
grep -Fq 'name: mlflow-system' "$NAMESPACE"
grep -Fq 'kind: StatefulSet' "$DATABASE"
grep -Fq 'name: mlflow-postgres' "$DATABASE"
grep -Fq 'storageClassName: ebs-ssd' "$DATABASE"
grep -Fq 'storage: 20Gi' "$DATABASE"
grep -Fq 'artifactsDestination: s3://vke-cluster/ray-train/platform/mlflow-artifacts' "$VALUES"
grep -Fq 'name: AWS_ACCESS_KEY_ID' "$VALUES"
grep -Fq 'name: MLFLOW_S3_ENDPOINT_URL' "$VALUES"
grep -Fq 'name: MLFLOW_BOTO_CLIENT_ADDRESSING_STYLE' "$VALUES"
grep -Fq 'value: virtual' "$VALUES"
grep -Fq 'mlflow.mlflow-system.svc.cluster.local:5000' "$VALUES"
grep -Fq '172.28.*' "$VALUES"
grep -Fq 'name: AWS_CONFIG_FILE' "$VALUES"
grep -Fq 'addressing_style = virtual' "$POLICY"
grep -Fq 'name: mlflow-ingest' "$POLICY"
grep -Fq 'return 403' "$POLICY"
grep -Fq 'location = /api/2.0/mlflow/runs/log-batch' "$POLICY"
grep -Fq 'app.kubernetes.io/managed-by: ray-train-platform' "$POLICY"
grep -Fq 'key: ray.io/tenant-id' "$POLICY"
grep -Fq 'operator: Exists' "$POLICY"
grep -Fq 'kubernetes.io/metadata.name: ray-train-platform' "$POLICY"
grep -Fq 'app: ray-train-backend' "$POLICY"
grep -Fq 'name: mlflow-postgres' "$POLICY"
grep -Fq 'cidr: 100.64.0.0/10' "$POLICY"
grep -Fq 'path: /metrics' "$POLICY"
grep -Fq 'mlflow-ingest.mlflow-system.svc.cluster.local:8080' "$SMOKE"
grep -Fq 'MLFLOW_ARTIFACT_DOWNLOAD_BLOCKED' "$SMOKE"
if grep -Fq 'mlflow.log_dict' "$SMOKE"; then
  echo 'training clients must not use MLflow as an artifact download surface' >&2
  exit 1
fi
if grep -Fq 'namespaceSelector: {}' "$POLICY"; then
  echo 'MLflow policy must not allow every namespace' >&2
  exit 1
fi
if grep -Eq '^[[:space:]]*type:[[:space:]]*(NodePort|LoadBalancer)[[:space:]]*$' "$VALUES"; then
  echo 'MLflow service must remain internal-only' >&2
  exit 1
fi
if grep -Fq 'static-prefix' "$VALUES"; then
  echo 'MLflow static prefix must use server.staticPrefix, not a flag_options duplicate' >&2
  exit 1
fi
grep -Fq -- '--static-prefix=/mlflow' "$VERIFY"
grep -Fq 'containers[?(@.name=="mlflow")].command' "$VERIFY" || {
  echo 'MLflow live verification must inspect the mlflow container command' >&2
  exit 1
}
grep -Fq 'containers[?(@.name=="mlflow")].args' "$VERIFY" || {
  echo 'MLflow live verification must inspect the mlflow container args' >&2
  exit 1
}
grep -Fq '/proxy/mlflow/health' "$VERIFY"
grep -Fq ".spec.replicas" "$VERIFY"
if grep -Fq 'get ingress' "$VERIFY"; then
  echo 'MLflow live verification must not depend on direct Ingress ownership' >&2
  exit 1
fi
echo 'MLflow delivery contract verified'
