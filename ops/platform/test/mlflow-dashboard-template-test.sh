#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly CHART="${ROOT_DIR}/helm/ray-train-platform"
readonly VALUES="${CHART}/values.yaml"
readonly TEMPLATE="${CHART}/templates/backend-deployment.yaml"
readonly PROFILE="${ROOT_DIR}/deploy/profiles/vke-cpu-ha.yaml"

assert_source_contract() {
  grep -Fq '    dashboardEnabled: false' "$VALUES" || {
    echo 'base values must disable the MLflow dashboard by default' >&2
    exit 1
  }
  grep -Fq '    publicOrigin: ""' "$VALUES" || {
    echo 'base values must default the MLflow dashboard public origin to empty' >&2
    exit 1
  }
  grep -Fq '    dashboardSessionHours: 8' "$VALUES" || {
    echo 'base values must default MLflow dashboard sessions to 8 hours' >&2
    exit 1
  }
  grep -Fq '    trackingURL: "http://mlflow.mlflow-system.svc.cluster.local:5000/mlflow"' "$VALUES" || {
    echo 'platform MLflow tracking URL must include the server /mlflow prefix' >&2
    exit 1
  }
  grep -Fq 'value: {{ default false $mlflow.dashboardEnabled | quote }}' "$TEMPLATE" || {
    echo 'backend template must default MLFLOW_DASHBOARD_ENABLED to false' >&2
    exit 1
  }
  grep -Fq 'value: {{ default "" $mlflow.publicOrigin | quote }}' "$TEMPLATE" || {
    echo 'backend template must default MLFLOW_PUBLIC_ORIGIN to empty' >&2
    exit 1
  }
  grep -Fq 'value: {{ default 8 $mlflow.dashboardSessionHours | quote }}' "$TEMPLATE" || {
    echo 'backend template must default MLFLOW_DASHBOARD_SESSION_HOURS to 8' >&2
    exit 1
  }
  grep -Fq '    dashboardEnabled: true' "$PROFILE" || {
    echo 'VKE production profile must enable the MLflow dashboard' >&2
    exit 1
  }
  grep -Fq '    publicOrigin: https://raytrain.wellspiking.ai' "$PROFILE" || {
    echo 'VKE production profile must set the production dashboard origin' >&2
    exit 1
  }
  grep -Fq '    dashboardSessionHours: 8' "$PROFILE" || {
    echo 'VKE production profile must use 8-hour MLflow dashboard sessions' >&2
    exit 1
  }
  grep -Fq '    trackingURL: http://mlflow.mlflow-system.svc.cluster.local:5000/mlflow' "$PROFILE" || {
    echo 'VKE production profile must preserve the MLflow server /mlflow prefix' >&2
    exit 1
  }
  if grep -Eq '^[[:space:]]*type:[[:space:]]*NodePort[[:space:]]*$' "$VALUES" "$PROFILE"; then
    echo 'VKE production backend/frontend services must not use NodePort' >&2
    exit 1
  fi
}

assert_rendered_env() {
  local manifest="$1"
  local name="$2"
  local expected="$3"

  awk -v name="$name" -v expected="$expected" '
    $0 ~ "^[[:space:]]*- name: " name "$" { found_name=1; next }
    found_name && $0 ~ "^[[:space:]]*value: \"" expected "\"$" { found_value=1; exit }
    found_name && $0 ~ "^[[:space:]]*- name:" { exit }
    END { exit !found_value }
  ' "$manifest" || {
    echo "rendered backend is missing ${name}=${expected}" >&2
    exit 1
  }
}

assert_source_contract

if [[ "${1:-}" == "--static-only" ]]; then
  echo 'MLflow dashboard static template contract verified'
  exit 0
fi

if ! command -v helm >/dev/null 2>&1; then
  echo 'Helm is required to run the MLflow dashboard rendered-template contract' >&2
  exit 1
fi

rendered="$(mktemp)"
trap 'rm -f "$rendered"' EXIT
helm template ray-train-platform "$CHART" --values "$PROFILE" >"$rendered"

assert_rendered_env "$rendered" MLFLOW_DASHBOARD_ENABLED true
assert_rendered_env "$rendered" MLFLOW_PUBLIC_ORIGIN https://raytrain.wellspiking.ai
assert_rendered_env "$rendered" MLFLOW_DASHBOARD_SESSION_HOURS 8
assert_rendered_env "$rendered" MLFLOW_TRACKING_URL http://mlflow.mlflow-system.svc.cluster.local:5000/mlflow
if grep -Eq '^[[:space:]]*type:[[:space:]]*NodePort[[:space:]]*$' "$rendered"; then
  echo 'rendered backend/frontend services must not use NodePort' >&2
  exit 1
fi

echo 'MLflow dashboard rendered-template contract verified'
