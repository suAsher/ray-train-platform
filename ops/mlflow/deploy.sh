#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly NAMESPACE="mlflow-system"
readonly PLATFORM_NAMESPACE="ray-train-platform"
readonly RELEASE="mlflow"
readonly CHART="${ROOT_DIR}/helm/vendor/mlflow-0.1.0.tgz"
readonly VALUES="${ROOT_DIR}/ops/mlflow/values-vke.yaml"
readonly ARTIFACT_STORAGE="${ROOT_DIR}/ops/mlflow/15-artifact-storage.yaml"
readonly ARTIFACT_ACCEPTANCE="${ROOT_DIR}/ops/mlflow/25-artifact-acceptance.yaml"
readonly TRANSITION_POLICY="${ROOT_DIR}/ops/mlflow/29-storage-migration-policy.yaml"
readonly TIMEOUT="${MLFLOW_DEPLOY_TIMEOUT:-15m}"
readonly CHART_SHA256="db32bf8f17be693a59f8c440d47a97fbea5a93c02d2b5e9ee1761efee50597e8"

main() {
  for command in kubectl helm grep sha256sum jq openssl base64; do
    command -v "$command" >/dev/null || { echo "missing command: ${command}" >&2; exit 1; }
  done
  [[ -f "$CHART" ]] || { echo "missing vendored chart: ${CHART}" >&2; exit 1; }
  [[ "$(sha256sum "$CHART" | awk '{print $1}')" == "$CHART_SHA256" ]] || { echo "vendored MLflow chart checksum mismatch" >&2; exit 1; }
  grep -Fq '@sha256:' "$VALUES" || { echo "MLflow image must be pinned by digest" >&2; exit 1; }

  kubectl apply -f "${ROOT_DIR}/ops/mlflow/00-namespace.yaml" >/dev/null
  copy_secret harbor-registry

  kubectl -n "$PLATFORM_NAMESPACE" get secret tos-fsx-credentials >/dev/null 2>&1 || {
    echo "missing CSI credential: ${PLATFORM_NAMESPACE}/tos-fsx-credentials" >&2
    exit 1
  }

  # NetworkPolicies are additive. Install the transition policy first so a
  # previously tightened policy cannot strand a rolled-back S3-backed Pod.
  kubectl apply -f "$TRANSITION_POLICY" >/dev/null
  local current_deployment
  current_deployment="$(kubectl -n "$NAMESPACE" get deployment mlflow --ignore-not-found -o yaml)"
  if grep -Eq 'tos-credentials|mlflow-aws-config|AWS_ACCESS_KEY_ID' <<<"$current_deployment"; then
    restore_legacy_dependencies || {
      echo 'failed to preserve dependencies required by the running MLflow release' >&2
      exit 1
    }
  fi

  kubectl apply -f "$ARTIFACT_STORAGE" >/dev/null
  kubectl -n "$NAMESPACE" wait \
    --for=jsonpath='{.status.phase}'=Bound \
    pvc/mlflow-artifacts \
    --timeout="$TIMEOUT"

  # Probe the FSX mount while the old release and all of its dependencies are
  # still untouched.
  run_job mlflow-artifact-storage-probe "${ROOT_DIR}/ops/mlflow/20-bootstrap.yaml"

  # Generate the dedicated database credential once. Re-deployments preserve it.
  if ! kubectl -n "$NAMESPACE" get secret mlflow-database >/dev/null 2>&1; then
    local database_user="mlflow"
    local database_name="mlflow"
    local database_password
    local database_uri
    database_password="$(openssl rand -hex 32)"
    database_uri="postgresql://${database_user}:${database_password}@mlflow-postgres:5432/${database_name}"
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

  # Database migrations are serialized before the two new server replicas start.
  run_job mlflow-db-upgrade "${ROOT_DIR}/ops/mlflow/22-db-upgrade.yaml"

  local previous_revision=""
  if helm -n "$NAMESPACE" status "$RELEASE" >/dev/null 2>&1; then
    previous_revision="$(helm -n "$NAMESPACE" history "$RELEASE" --max 1 -o json | jq -r '.[0].revision // empty')"
  fi

  # --atomic handles chart rollout failures. Legacy dependencies and transition
  # egress remain available because cleanup has not started yet.
  if ! helm upgrade --install "$RELEASE" "$CHART" \
    --namespace "$NAMESPACE" \
    --create-namespace \
    --values "$VALUES" \
    --atomic --wait --timeout "$TIMEOUT"; then
    echo 'MLflow Helm rollout failed; atomic rollback retained legacy storage dependencies' >&2
    exit 1
  fi

  # If the preflight history lookup was unavailable but the upgrade succeeded,
  # derive the rollback target from the now-deployed revision before acceptance.
  if [[ -z "$previous_revision" ]]; then
    local deployed_revision
    deployed_revision="$(helm -n "$NAMESPACE" history "$RELEASE" --max 1 -o json | jq -r '.[0].revision // empty')"
    if [[ "$deployed_revision" =~ ^[0-9]+$ ]] && (( deployed_revision > 1 )); then
      previous_revision=$((deployed_revision - 1))
    fi
  fi

  # A healthy Pod is insufficient: prove the new server can create metadata and
  # upload, download, and delete an artifact through the tracking API.
  if ! run_job mlflow-artifact-acceptance "$ARTIFACT_ACCEPTANCE"; then
    if [[ -n "$previous_revision" ]] && rollback_to_revision "$previous_revision"; then
      echo "artifact acceptance failed; restored Helm revision ${previous_revision}" >&2
    else
      echo 'artifact acceptance failed; legacy dependencies and transition egress were retained for recovery' >&2
    fi
    exit 1
  fi

  if ! cleanup_legacy_dependencies; then
    echo 'post-acceptance cleanup failed; automatic recovery retained/restored legacy dependencies and transition egress' >&2
    exit 1
  fi

  bash "${ROOT_DIR}/ops/mlflow/verify.sh"
}

encode() {
  printf '%s' "$1" | base64 | tr -d '\n'
}

copy_secret() {
  local name="$1"
  kubectl -n "$PLATFORM_NAMESPACE" get secret "$name" -o json | jq \
    --arg namespace "$NAMESPACE" \
    '{apiVersion:"v1",kind:"Secret",metadata:{name:.metadata.name,namespace:$namespace},type:.type,data:.data}' | \
    kubectl apply -f - >/dev/null
}

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

apply_legacy_aws_config() {
  kubectl -n "$NAMESPACE" create configmap mlflow-aws-config \
    --from-literal=config=$'[default]\nregion = cn-shanghai\ns3 =\n    addressing_style = virtual\n' \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
}

restore_legacy_dependencies() {
  local status=0
  kubectl apply -f "${TRANSITION_POLICY}" >/dev/null || status=1
  copy_secret tos-credentials || status=1
  apply_legacy_aws_config || status=1
  return "$status"
}

rollback_to_revision() {
  local revision="$1"
  restore_legacy_dependencies || {
    echo 'failed to restore legacy MLflow dependencies before Helm rollback' >&2
    return 1
  }
  helm rollback "$RELEASE" "$revision" --namespace "$NAMESPACE" --wait --timeout "$TIMEOUT" || return 1
  kubectl -n "$NAMESPACE" rollout status deployment/mlflow --timeout="$TIMEOUT" || return 1
  local health
  health="$(kubectl get --raw '/api/v1/namespaces/mlflow-system/services/http:mlflow:5000/proxy/mlflow/health')" || return 1
  [[ "$health" == "OK" || "$health" == *'status'* ]]
}

recover_cleanup_failure() {
  if ! restore_legacy_dependencies; then
    echo 'automatic recovery failed; reapply 29-storage-migration-policy.yaml and restore mlflow-system/tos-credentials plus mlflow-aws-config before rollback' >&2
  fi
  return 1
}

cleanup_legacy_dependencies() {
  kubectl apply -f "${ROOT_DIR}/ops/mlflow/30-policy.yaml" >/dev/null || return 1
  if ! kubectl -n "$NAMESPACE" delete job mlflow-tos-prefix-init --ignore-not-found >/dev/null; then recover_cleanup_failure; return 1; fi
  if ! kubectl -n "$NAMESPACE" delete secret tos-credentials --ignore-not-found >/dev/null; then recover_cleanup_failure; return 1; fi
  if ! kubectl -n "$NAMESPACE" delete configmap mlflow-aws-config --ignore-not-found >/dev/null; then recover_cleanup_failure; return 1; fi
  if ! kubectl -n "$NAMESPACE" delete networkpolicy mlflow-storage-migration --ignore-not-found >/dev/null; then recover_cleanup_failure; return 1; fi
}

main "$@"
