#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly NAMESPACE="mlflow-system"
readonly PLATFORM_NAMESPACE="ray-train-platform"
readonly RELEASE="mlflow"
readonly CHART="${ROOT_DIR}/helm/vendor/mlflow-0.1.0.tgz"
readonly VALUES="${ROOT_DIR}/ops/mlflow/values-vke.yaml"
readonly TIMEOUT="${MLFLOW_DEPLOY_TIMEOUT:-15m}"
readonly CHART_SHA256="db32bf8f17be693a59f8c440d47a97fbea5a93c02d2b5e9ee1761efee50597e8"

for command in kubectl helm grep sha256sum jq openssl base64; do
  command -v "$command" >/dev/null || { echo "missing command: ${command}" >&2; exit 1; }
done
[[ -f "$CHART" ]] || { echo "missing vendored chart: ${CHART}" >&2; exit 1; }
[[ "$(sha256sum "$CHART" | awk '{print $1}')" == "$CHART_SHA256" ]] || { echo "vendored MLflow chart checksum mismatch" >&2; exit 1; }
grep -Fq '@sha256:' "$VALUES" || { echo "MLflow image must be pinned by digest" >&2; exit 1; }

kubectl apply -f "${ROOT_DIR}/ops/mlflow/00-namespace.yaml" >/dev/null

copy_secret() {
  local name="$1"
  kubectl -n "$PLATFORM_NAMESPACE" get secret "$name" -o json | jq \
    --arg namespace "$NAMESPACE" \
    '{apiVersion:"v1",kind:"Secret",metadata:{name:.metadata.name,namespace:$namespace},type:.type,data:.data}' | \
    kubectl apply -f - >/dev/null
}

# Keep registry and TOS credentials isolated in the MLflow namespace. They are
# copied without printing values and are never mounted into training Pods.
copy_secret harbor-registry
copy_secret tos-credentials

# Generate the dedicated database credential once. Re-deployments preserve it.
if ! kubectl -n "$NAMESPACE" get secret mlflow-database >/dev/null 2>&1; then
  database_user="mlflow"
  database_name="mlflow"
  database_password="$(openssl rand -hex 32)"
  database_uri="postgresql://${database_user}:${database_password}@mlflow-postgres:5432/${database_name}"
  encode() { printf '%s' "$1" | base64 | tr -d '\n'; }
  printf '%s\n' \
    'apiVersion: v1' \
    'kind: Secret' \
    'metadata:' \
    '  name: mlflow-database' \
    "  namespace: $NAMESPACE" \
    '  labels:' \
    '    app.kubernetes.io/name: mlflow-postgres' \
    '    app.kubernetes.io/part-of: ray-train-platform' \
    'type: Opaque' \
    'data:' \
    "  username: $(encode "$database_user")" \
    "  password: $(encode "$database_password")" \
    "  database: $(encode "$database_name")" \
    "  uri: $(encode "$database_uri")" | kubectl apply -f - >/dev/null
  unset database_user database_name database_password database_uri
fi

kubectl apply -f "${ROOT_DIR}/ops/mlflow/10-database.yaml" >/dev/null
kubectl -n "$NAMESPACE" rollout status statefulset/mlflow-postgres --timeout="$TIMEOUT"

run_job() {
  local name="$1"
  local manifest="$2"
  kubectl -n "$NAMESPACE" delete job "$name" --ignore-not-found >/dev/null
  kubectl apply -f "$manifest" >/dev/null
  if ! kubectl -n "$NAMESPACE" wait --for=condition=complete "job/${name}" --timeout="$TIMEOUT"; then
    kubectl -n "$NAMESPACE" logs "job/${name}" --all-containers=true --tail=200 || true
    return 1
  fi
}

# Seed the object prefix so bucket policy/path mistakes fail before rollout.
run_job mlflow-tos-prefix-init "${ROOT_DIR}/ops/mlflow/20-bootstrap.yaml"

# Database migrations are serialized before the two server replicas start.
run_job mlflow-db-upgrade "${ROOT_DIR}/ops/mlflow/22-db-upgrade.yaml"

# Install the deny-by-default policy before the first server Pod exists.
kubectl apply -f "${ROOT_DIR}/ops/mlflow/30-policy.yaml" >/dev/null
helm upgrade --install "$RELEASE" "$CHART" \
  --namespace "$NAMESPACE" \
  --create-namespace \
  --values "$VALUES" \
  --atomic --wait --timeout "$TIMEOUT"

bash "${ROOT_DIR}/ops/mlflow/verify.sh"
