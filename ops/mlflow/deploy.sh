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
readonly LEASE_NAME="mlflow-deploy"
readonly CHART_SHA256="db32bf8f17be693a59f8c440d47a97fbea5a93c02d2b5e9ee1761efee50597e8"

DEPLOY_RUN_ID=""
LEASE_HOLDER=""
LEASE_ACQUIRED=false
LEASE_RELEASE_BLOCKED=false

main() {
  for command in kubectl helm grep sha256sum jq openssl base64; do
    command -v "$command" >/dev/null || { echo "missing command: ${command}" >&2; exit 1; }
  done
  [[ -f "$CHART" ]] || { echo "missing vendored chart: ${CHART}" >&2; exit 1; }
  [[ "$(sha256sum "$CHART" | awk '{print $1}')" == "$CHART_SHA256" ]] || { echo "vendored MLflow chart checksum mismatch" >&2; exit 1; }
  grep -Fq '@sha256:' "$VALUES" || { echo "MLflow image must be pinned by digest" >&2; exit 1; }
  verify_fsx_irsa

  kubectl apply -f "${ROOT_DIR}/ops/mlflow/00-namespace.yaml" >/dev/null
  initialize_deploy_identity
  install_deploy_traps
  acquire_deploy_lease || {
    echo 'another MLflow deployment is active or the deployment Lease could not be acquired' >&2
    exit 1
  }

  copy_secret harbor-registry

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
    pvc/mlflow-artifacts-irsa \
    --timeout="$TIMEOUT"

  # Probe the FSX mount while the old release and all of its dependencies are
  # still untouched.
  run_job "$(deployment_job_name mlflow-artifact-storage-probe)" "${ROOT_DIR}/ops/mlflow/20-bootstrap.yaml"

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
  run_job "$(deployment_job_name mlflow-db-upgrade)" "${ROOT_DIR}/ops/mlflow/22-db-upgrade.yaml"

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
  if ! run_job "$(deployment_job_name mlflow-artifact-acceptance)" "$ARTIFACT_ACCEPTANCE"; then
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

verify_fsx_irsa() {
  local fsx_daemonset

  kubectl get csidriver fsx.csi.volcengine.com >/dev/null 2>&1 || {
    echo 'missing CSIDriver fsx.csi.volcengine.com' >&2
    return 1
  }
  if ! fsx_daemonset="$(kubectl -n kube-system get daemonset csi-fsx-node -o json)"; then
    echo 'missing FSX CSI node DaemonSet kube-system/csi-fsx-node' >&2
    return 1
  fi
  if ! jq -e '
    (.status.desiredNumberScheduled // 0) > 0 and
    (.status.numberAvailable // 0) == (.status.desiredNumberScheduled // 0)
  ' <<<"$fsx_daemonset" >/dev/null; then
    echo 'FSX CSI node DaemonSet is not fully available' >&2
    return 1
  fi
  if ! jq -e '
    any(.spec.template.spec.containers[]?;
      .name == "driver" and
      any(.env[]?; .name == "CREDENTIALS_TYPE" and ((.value // "") | ascii_downcase) == "irsa") and
      any(.env[]?; .name == "ROLE_NAME_FOR_IRSA" and ((.value // "") | length > 0))
    )
  ' <<<"$fsx_daemonset" >/dev/null; then
    echo 'FSX CSI node driver must use IRSA with a non-empty ROLE_NAME_FOR_IRSA' >&2
    return 1
  fi
}

initialize_deploy_identity() {
  if ! DEPLOY_RUN_ID="$(generate_deploy_run_id)"; then
    echo 'failed to generate a unique MLflow deployment run ID' >&2
    return 1
  fi
  if ! [[ "$DEPLOY_RUN_ID" =~ ^[a-z0-9]([a-z0-9-]{0,18}[a-z0-9])?$ ]]; then
    echo 'generated MLflow deployment run ID is not a lowercase DNS label of at most 20 characters' >&2
    return 1
  fi
  LEASE_HOLDER="mlflow-deploy-${DEPLOY_RUN_ID}"
}

generate_deploy_run_id() {
  openssl rand -hex 8
}

generate_job_request_nonce() {
  openssl rand -hex 8
}

deployment_job_name() {
  local name="${1}-${DEPLOY_RUN_ID}"
  if (( ${#name} > 63 )); then
    echo "generated Job name exceeds 63 characters: ${name}" >&2
    return 1
  fi
  printf '%s' "$name"
}

acquire_deploy_lease() {
  local existing_lease
  local current_holder
  local lease_payload
  local acquired_lease

  if ! existing_lease="$(kubectl -n "$NAMESPACE" get lease "$LEASE_NAME" --ignore-not-found -o json)"; then
    echo "failed to inspect deployment Lease ${NAMESPACE}/${LEASE_NAME}" >&2
    return 1
  fi
  if [[ -z "$existing_lease" ]]; then
    if ! lease_payload="$(jq -n \
      --arg name "$LEASE_NAME" \
      --arg namespace "$NAMESPACE" \
      --arg holder "$LEASE_HOLDER" \
      '{apiVersion:"coordination.k8s.io/v1",kind:"Lease",metadata:{name:$name,namespace:$namespace},spec:{holderIdentity:$holder,leaseTransitions:0}}')"; then
      echo 'failed to render deployment Lease' >&2
      return 1
    fi
    if ! acquired_lease="$(kubectl -n "$NAMESPACE" create -f - -o json <<<"$lease_payload")"; then
      echo "failed to create deployment Lease ${NAMESPACE}/${LEASE_NAME}; another deploy may have won the race" >&2
      return 1
    fi
    LEASE_ACQUIRED=true
  else
    if ! current_holder="$(jq -er '.spec.holderIdentity // ""' <<<"$existing_lease")"; then
      echo "deployment Lease ${NAMESPACE}/${LEASE_NAME} has an invalid holderIdentity" >&2
      return 1
    fi
    if [[ -n "$current_holder" ]]; then
      echo "deployment Lease ${NAMESPACE}/${LEASE_NAME} has non-empty holder ${current_holder}; automatic takeover is disabled" >&2
      echo 'confirm no MLflow deployment is running, then follow the README manual unlock procedure' >&2
      return 1
    fi
    if ! lease_payload="$(jq \
      --arg holder "$LEASE_HOLDER" '
        .spec = (.spec // {}) |
        .spec.holderIdentity = $holder |
        .spec.leaseTransitions = ((.spec.leaseTransitions // 0) + 1)
      ' <<<"$existing_lease")"; then
      echo 'failed to render deployment Lease takeover' >&2
      return 1
    fi
    if ! acquired_lease="$(kubectl -n "$NAMESPACE" replace -f - -o json <<<"$lease_payload")"; then
      echo "failed to acquire deployment Lease ${NAMESPACE}/${LEASE_NAME}; resourceVersion changed" >&2
      return 1
    fi
    LEASE_ACQUIRED=true
  fi

  if ! jq -e --arg name "$LEASE_NAME" --arg holder "$LEASE_HOLDER" \
    '.metadata.name == $name and .spec.holderIdentity == $holder and (.metadata.resourceVersion | type == "string")' \
    <<<"$acquired_lease" >/dev/null; then
    echo "deployment Lease ${NAMESPACE}/${LEASE_NAME} response did not confirm this holder" >&2
    return 1
  fi
  return 0
}

release_deploy_lease() {
  local current_lease
  local current_holder
  local release_payload
  local released_lease

  if [[ "$LEASE_ACQUIRED" != true ]]; then
    return 0
  fi
  if ! current_lease="$(kubectl -n "$NAMESPACE" get lease "$LEASE_NAME" -o json)"; then
    echo "failed to read deployment Lease ${NAMESPACE}/${LEASE_NAME} for release" >&2
    return 1
  fi
  if ! current_holder="$(jq -er '.spec.holderIdentity // ""' <<<"$current_lease")"; then
    echo "deployment Lease ${NAMESPACE}/${LEASE_NAME} has an invalid holder during release" >&2
    return 1
  fi
  if [[ "$current_holder" != "$LEASE_HOLDER" ]]; then
    LEASE_ACQUIRED=false
    echo "refusing to release deployment Lease owned by ${current_holder:-no holder}; expected ${LEASE_HOLDER}" >&2
    return 1
  fi
  if ! release_payload="$(jq '.spec.holderIdentity = ""' <<<"$current_lease")"; then
    echo 'failed to render deployment Lease release' >&2
    return 1
  fi
  if ! released_lease="$(kubectl -n "$NAMESPACE" replace -f - -o json <<<"$release_payload")"; then
    echo "failed to release deployment Lease ${NAMESPACE}/${LEASE_NAME}; resourceVersion changed, so no retry was attempted" >&2
    return 1
  fi
  if ! jq -e '.spec.holderIdentity == ""' <<<"$released_lease" >/dev/null; then
    echo "deployment Lease ${NAMESPACE}/${LEASE_NAME} release was not confirmed" >&2
    return 1
  fi
  LEASE_ACQUIRED=false
  return 0
}

deployment_exit_trap() {
  local status="$1"
  trap - EXIT INT TERM HUP
  if [[ "$LEASE_ACQUIRED" == true ]]; then
    if [[ "$LEASE_RELEASE_BLOCKED" == true ]]; then
      echo "deployment Lease ${NAMESPACE}/${LEASE_NAME} retained for manual recovery by holder ${LEASE_HOLDER}" >&2
    elif ! release_deploy_lease; then
      echo 'failed to release the MLflow deployment Lease safely' >&2
      if (( status == 0 )); then
        status=1
      fi
    fi
  fi
  exit "$status"
}

deployment_signal_trap() {
  local signal_name="$1"
  local status="$2"

  if [[ "$LEASE_ACQUIRED" == true ]]; then
    LEASE_RELEASE_BLOCKED=true
    echo "received ${signal_name} while holding deployment Lease ${NAMESPACE}/${LEASE_NAME}; fail-closed interruption" >&2
    echo 'Confirm that no MLflow Job/Pod or Helm rollout is still active, then follow the README manual recovery procedure.' >&2
  fi
  exit "$status"
}

install_deploy_traps() {
  trap 'deployment_exit_trap $?' EXIT
  trap 'deployment_signal_trap INT 130' INT
  trap 'deployment_signal_trap TERM 143' TERM
  trap 'deployment_signal_trap HUP 129' HUP
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

cleanup_job_instance() {
  local name="$1"
  local expected_uid="$2"
  local expected_run_id="${3:-}"
  local delete_options
  local pod_selector="job-name=${name}"
  local matching_pods

  if ! delete_options="$(jq -n --arg uid "$expected_uid" \
    '{apiVersion:"v1",kind:"DeleteOptions",propagationPolicy:"Foreground",preconditions:{uid:$uid}}')"; then
    echo "failed to render UID-preconditioned deletion for Job ${name}" >&2
    retain_deploy_lease_for_manual_recovery "$name" 'failed to render Foreground UID-preconditioned deletion'
    return 1
  fi
  if ! kubectl delete \
    --raw "/apis/batch/v1/namespaces/${NAMESPACE}/jobs/${name}" \
    -f - <<<"$delete_options" >/dev/null; then
    echo "failed to terminate Job ${name} with UID ${expected_uid}; it may have been replaced" >&2
    retain_deploy_lease_for_manual_recovery "$name" 'Foreground UID-preconditioned delete failed'
    return 1
  fi
  if ! kubectl -n "$NAMESPACE" wait --for=delete "job/${name}" --timeout="$TIMEOUT" >/dev/null; then
    retain_deploy_lease_for_manual_recovery "$name" 'Job did not reach NotFound after Foreground deletion'
    return 1
  fi
  if [[ -n "$expected_run_id" ]]; then
    pod_selector+=",platform.wellspiking.ai/deploy-run-id=${expected_run_id}"
  fi
  if ! matching_pods="$(kubectl -n "$NAMESPACE" get pods -l "$pod_selector" -o json)"; then
    retain_deploy_lease_for_manual_recovery "$name" 'failed to list Pods after Job deletion'
    return 1
  fi
  if ! jq -e '.items | type == "array"' <<<"$matching_pods" >/dev/null; then
    retain_deploy_lease_for_manual_recovery "$name" 'Pod deletion query returned an invalid response'
    return 1
  fi
  if ! jq -e '.items | length == 0' <<<"$matching_pods" >/dev/null; then
    if ! kubectl -n "$NAMESPACE" wait --for=delete pod -l "$pod_selector" --timeout="$TIMEOUT" >/dev/null; then
      retain_deploy_lease_for_manual_recovery "$name" 'Pods did not disappear after Foreground Job deletion'
      return 1
    fi
  fi
  if ! matching_pods="$(kubectl -n "$NAMESPACE" get pods -l "$pod_selector" -o json)"; then
    retain_deploy_lease_for_manual_recovery "$name" 'failed to confirm Pod deletion'
    return 1
  fi
  if ! jq -e '.items | type == "array" and length == 0' <<<"$matching_pods" >/dev/null; then
    retain_deploy_lease_for_manual_recovery "$name" 'Pods remain after Foreground Job deletion'
    return 1
  fi
  return 0
}

retain_deploy_lease_for_manual_recovery() {
  local job_name="$1"
  local reason="$2"

  LEASE_RELEASE_BLOCKED=true
  echo "Lease retained: ${NAMESPACE}/${LEASE_NAME} holder ${LEASE_HOLDER} because Job ${job_name} is uncertain (${reason})." >&2
  echo "Inspect Job ${NAMESPACE}/${job_name} and confirm no MLflow deployment is running; then follow the README manual unlock procedure." >&2
}

run_job() {
  local name="$1"
  local manifest="$2"
  local rendered_job
  local created_job
  local created_name
  local created_uid
  local current_uid
  local completed_job
  local observed_job
  local request_nonce

  if ! rendered_job="$(kubectl -n "$NAMESPACE" create --dry-run=client -f "$manifest" -o json)"; then
    echo "failed to render unique Job ${name} from ${manifest}" >&2
    return 1
  fi
  if ! request_nonce="$(generate_job_request_nonce "$name")"; then
    echo "failed to generate request nonce for Job ${name}" >&2
    return 1
  fi
  if ! [[ "$request_nonce" =~ ^[a-z0-9]([a-z0-9-]{0,18}[a-z0-9])?$ ]]; then
    echo "generated request nonce for Job ${name} is not a lowercase DNS label of at most 20 characters" >&2
    return 1
  fi
  if ! rendered_job="$(jq \
    --arg name "$name" \
    --arg run_id "$DEPLOY_RUN_ID" \
    --arg request_nonce "$request_nonce" '
      .metadata.name = $name |
      .metadata.labels = ((.metadata.labels // {}) + {
        "platform.wellspiking.ai/deploy-run-id":$run_id,
        "platform.wellspiking.ai/request-nonce":$request_nonce
      }) |
      .spec.template.metadata = (.spec.template.metadata // {}) |
      .spec.template.metadata.labels = ((.spec.template.metadata.labels // {}) + {
        "platform.wellspiking.ai/deploy-run-id":$run_id,
        "platform.wellspiking.ai/request-nonce":$request_nonce
      }) |
      del(.metadata.creationTimestamp, .metadata.resourceVersion, .metadata.uid, .status)
    ' <<<"$rendered_job")"; then
    echo "failed to assign unique identity to Job ${name}" >&2
    return 1
  fi
  if created_job="$(kubectl -n "$NAMESPACE" create -f - -o json <<<"$rendered_job")"; then
    :
  else
    echo "create returned an error for Job ${name}; checking whether the API server persisted it" >&2
    if ! observed_job="$(kubectl -n "$NAMESPACE" get job "$name" --ignore-not-found -o json)"; then
      retain_deploy_lease_for_manual_recovery "$name" 'create failed and follow-up GET failed'
      return 1
    fi
    if [[ -z "$observed_job" ]]; then
      echo "create failed and Job ${name} was confirmed absent" >&2
      return 1
    fi
    if ! jq -e --arg name "$name" --arg run_id "$DEPLOY_RUN_ID" --arg request_nonce "$request_nonce" '
      .metadata.name == $name and
      .metadata.labels["platform.wellspiking.ai/deploy-run-id"] == $run_id and
      .metadata.labels["platform.wellspiking.ai/request-nonce"] == $request_nonce and
      (.metadata.uid | type == "string" and length > 0)
    ' <<<"$observed_job" >/dev/null; then
      retain_deploy_lease_for_manual_recovery "$name" 'the observed object does not belong to this deploy run'
      return 1
    fi
    created_job="$observed_job"
    echo "recovered Job ${name} after an ambiguous create response" >&2
  fi
  if ! created_name="$(jq -r '.metadata.name // empty' <<<"$created_job")" ||
     ! created_uid="$(jq -r '.metadata.uid // empty' <<<"$created_job")"; then
    echo "failed to parse identity of fresh Job ${name}" >&2
    retain_deploy_lease_for_manual_recovery "$name" 'created Job identity response could not be parsed'
    return 1
  fi
  if [[ "$created_name" != "$name" || -z "$created_uid" ]]; then
    echo "fresh Job identity mismatch for ${name}: name=${created_name:-missing}, uid=${created_uid:-missing}" >&2
    if [[ -n "$created_name" && -n "$created_uid" ]]; then
      cleanup_job_instance "$created_name" "$created_uid" "$DEPLOY_RUN_ID" || return 1
    else
      retain_deploy_lease_for_manual_recovery "$name" 'created Job identity was incomplete'
    fi
    return 1
  fi
  if ! current_uid="$(kubectl -n "$NAMESPACE" get job "$name" -o jsonpath='{.metadata.uid}')"; then
    echo "failed to read fresh Job ${name} with UID ${created_uid}" >&2
    cleanup_job_instance "$name" "$created_uid" "$DEPLOY_RUN_ID" || return 1
    return 1
  fi
  if [[ "$current_uid" != "$created_uid" ]]; then
    echo "fresh Job ${name} was replaced before wait: expected UID ${created_uid}, got ${current_uid}" >&2
    cleanup_job_instance "$name" "$created_uid" "$DEPLOY_RUN_ID" || return 1
    return 1
  fi
  if ! kubectl -n "$NAMESPACE" wait --for=condition=complete "job/${name}" --timeout="$TIMEOUT"; then
    kubectl -n "$NAMESPACE" logs "job/${name}" --all-containers=true --tail=200 || true
    cleanup_job_instance "$name" "$created_uid" "$DEPLOY_RUN_ID" || return 1
    return 1
  fi
  if ! completed_job="$(kubectl -n "$NAMESPACE" get job "$name" -o json)"; then
    echo "failed to verify completed Job ${name} with UID ${created_uid}" >&2
    cleanup_job_instance "$name" "$created_uid" "$DEPLOY_RUN_ID" || return 1
    return 1
  fi
  if ! jq -e --arg name "$name" --arg uid "$created_uid" \
    '.metadata.name == $name and .metadata.uid == $uid and any(.status.conditions[]?; .type == "Complete" and .status == "True")' \
    <<<"$completed_job" >/dev/null; then
    echo "Job ${name} completion did not belong to fresh UID ${created_uid}" >&2
    cleanup_job_instance "$name" "$created_uid" "$DEPLOY_RUN_ID" || return 1
    return 1
  fi
  return 0
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

cleanup_legacy_job() {
  local name="$1"
  local legacy_job
  local legacy_uid
  local legacy_run_id

  if ! legacy_job="$(kubectl -n "$NAMESPACE" get job "$name" --ignore-not-found -o json)"; then
    echo "failed to inspect legacy Job ${name}" >&2
    return 1
  fi
  if [[ -z "$legacy_job" ]]; then
    return 0
  fi
  if ! legacy_uid="$(jq -er '.metadata.uid // empty' <<<"$legacy_job")"; then
    echo "legacy Job ${name} has no UID; refusing name-only cleanup" >&2
    return 1
  fi
  if ! legacy_run_id="$(jq -r '.spec.template.metadata.labels["platform.wellspiking.ai/deploy-run-id"] // empty' <<<"$legacy_job")"; then
    echo "failed to read legacy Job ${name} Pod identity" >&2
    retain_deploy_lease_for_manual_recovery "$name" 'legacy Job Pod identity could not be read'
    return 1
  fi
  cleanup_job_instance "$name" "$legacy_uid" "$legacy_run_id"
}

cleanup_legacy_dependencies() {
  kubectl apply -f "${ROOT_DIR}/ops/mlflow/30-policy.yaml" >/dev/null || return 1
  if ! cleanup_legacy_job mlflow-tos-prefix-init; then recover_cleanup_failure; return 1; fi
  if ! kubectl -n "$NAMESPACE" delete secret tos-credentials --ignore-not-found >/dev/null; then recover_cleanup_failure; return 1; fi
  if ! kubectl -n "$NAMESPACE" delete configmap mlflow-aws-config --ignore-not-found >/dev/null; then recover_cleanup_failure; return 1; fi
  if ! kubectl -n "$NAMESPACE" delete networkpolicy mlflow-storage-migration --ignore-not-found >/dev/null; then recover_cleanup_failure; return 1; fi
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
