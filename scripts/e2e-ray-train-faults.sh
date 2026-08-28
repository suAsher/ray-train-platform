#!/usr/bin/env bash
set -euo pipefail

readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/e2e-training.sh
source "${script_dir}/e2e-training.sh"

readonly fault_cases=(worker-process-exit worker-pod-delete dns-transient tos-transient gpu-node-restart driver-head-failure)
readonly worker_selector='ray.io/node-type=worker'
readonly head_selector='ray.io/node-type=head'

require_fault_identity() {
  [[ "${ALLOW_DESTRUCTIVE_FAULT_TESTS:-0}" == 1 ]] || { e2e_die 'fault tests require ALLOW_DESTRUCTIVE_FAULT_TESTS=1'; return 1; }
  [[ "${ACCEPTANCE_JOB_ID:-}" =~ ^job-[0-9a-f]{24}$ ]] || { e2e_die 'ACCEPTANCE_JOB_ID must be one dedicated persisted acceptance job ID'; return 1; }
  [[ "${ACCEPTANCE_LABEL_VALUE:-}" == "$ACCEPTANCE_JOB_ID" ]] || { e2e_die 'ACCEPTANCE_LABEL_VALUE must exactly match ACCEPTANCE_JOB_ID'; return 1; }
  [[ "${FAULT_CASE:-}" =~ ^(worker-process-exit|worker-pod-delete|dns-transient|tos-transient|gpu-node-restart|driver-head-failure)$ ]] || { e2e_die 'FAULT_CASE must select exactly one supported fault'; return 1; }
  if [[ "$FAULT_CASE" == gpu-node-restart ]]; then
    [[ "${TARGET_GPU_NODE:-}" =~ ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$ ]] || { e2e_die 'TARGET_GPU_NODE must be one exact node name'; return 1; }
    [[ "${CONFIRM_GPU_NODE_RESTART:-}" == "$TARGET_GPU_NODE" ]] || { e2e_die 'CONFIRM_GPU_NODE_RESTART must exactly match TARGET_GPU_NODE'; return 1; }
  fi
}

require_fault_live_configuration() {
  e2e_live_enabled || { e2e_die 'destructive faults require RUN_RAY_TRAIN_E2E=1 and DRY_RUN=0'; return 1; }
  validate_tenant_namespace "${TENANT_NAMESPACE:-}" || return 1
  [[ "${API_URL:-}" =~ ^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?/?$ ]] || { e2e_die 'API_URL must be a concrete HTTPS origin'; return 1; }
  [[ -f "${SPK_RAYJOB_CONFIG:-}" && ! -L "${SPK_RAYJOB_CONFIG:-}" ]] || { e2e_die 'SPK_RAYJOB_CONFIG must be a regular file'; return 1; }
  command -v kubectl >/dev/null || { e2e_die 'kubectl is required'; return 1; }
  command -v spk-rayjob >/dev/null || { e2e_die 'spk-rayjob is required'; return 1; }
  command -v jq >/dev/null || { e2e_die 'jq is required'; return 1; }
  command -v perl >/dev/null || { e2e_die 'perl is required for bounded subprocesses'; return 1; }
  _secure_config_token "$SPK_RAYJOB_CONFIG" >/dev/null || return 1
}

exact_job_selector() {
  require_fault_identity || return
  [[ "${ACCEPTANCE_TENANT_ID:-}" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || { e2e_die 'ACCEPTANCE_TENANT_ID must match the persisted tenant'; return 1; }
  printf '%s=%s,%s=%s\n' "$ACCEPTANCE_LABEL_KEY" "$ACCEPTANCE_JOB_ID" "$ACCEPTANCE_TENANT_LABEL_KEY" "$ACCEPTANCE_TENANT_ID"
}

verify_acceptance_job() {
  local detail namespace engine name tenant_id
  detail="$(job_status_json "$ACCEPTANCE_JOB_ID")" || return
  namespace="$(jq -er '(.data // .).kubernetesNamespace' <<<"$detail")" || return
  engine="$(jq -er '(.data // .).spec.trainingEngine' <<<"$detail")" || return
  name="$(jq -er '(.data // .).spec.name' <<<"$detail")" || return
  tenant_id="$(jq -er '(.data // .).tenantId' <<<"$detail")" || return
  [[ "$namespace" == "$TENANT_NAMESPACE" ]] || { e2e_die 'acceptance job is outside the explicit tenant namespace'; return 1; }
  [[ "$engine" == ray-train ]] || { e2e_die 'destructive faults require a managed Ray Train acceptance job'; return 1; }
  [[ "$name" == "${ACCEPTANCE_PREFIX}-"* ]] || { e2e_die 'acceptance job name does not match ACCEPTANCE_PREFIX'; return 1; }
  [[ "$tenant_id" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || { e2e_die 'acceptance job tenant identity is unsafe'; return 1; }
  ACCEPTANCE_TENANT_ID="$tenant_id"
}

select_one_pod() {
  local node_type_selector="$1" selector response count pod
  selector="$(exact_job_selector),${node_type_selector}" || return
  [[ "$selector" != *'*'* && "$selector" != *','','* ]] || { e2e_die 'refusing an empty or broad selector'; return 1; }
  response="$(kube get pods --namespace "$TENANT_NAMESPACE" --selector "$selector" -o json)" || return
  count="$(jq '.items | length' <<<"$response")" || return
  [[ "$count" -gt 0 ]] || { e2e_die "no exact acceptance pod matched $selector"; return 1; }
  pod="$(jq -er '.items | sort_by(.metadata.name) | first | .metadata.name' <<<"$response")" || return
  [[ "$pod" =~ ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$ ]] || { e2e_die 'selected pod name is unsafe'; return 1; }
  printf '%s\n' "$pod"
}

verify_pod_ownership() {
  local pod="$1" identity
  identity="$(kube get pod "$pod" --namespace "$TENANT_NAMESPACE" -o 'jsonpath={.metadata.labels.platform_job_id}{"\t"}{.metadata.labels.platform_tenant_id}')" || return
  [[ "$identity" == "$ACCEPTANCE_JOB_ID"$'\t'"$ACCEPTANCE_TENANT_ID" ]] || { e2e_die "pod $pod lost its exact acceptance ownership labels"; return 1; }
}

worker_process_exit() {
  local pod
  pod="$(select_one_pod "$worker_selector")" || return
  verify_pod_ownership "$pod" || return
  ledger_record pod "$pod" "$TENANT_NAMESPACE" "$ACCEPTANCE_JOB_ID" || return
  kube exec "$pod" --namespace "$TENANT_NAMESPACE" -- sh -ceu 'pid="$(pgrep -f "ray::.*TrainWorker" | head -n 1)"; test -n "$pid"; kill -TERM "$pid"'
}

worker_pod_delete() {
  local pod
  pod="$(select_one_pod "$worker_selector")" || return
  verify_pod_ownership "$pod" || return
  ledger_record pod "$pod" "$TENANT_NAMESPACE" "$ACCEPTANCE_JOB_ID" || return
  kube delete pod "$pod" --namespace "$TENANT_NAMESPACE" --wait=false
}

apply_dns_transient() {
  local name="${ACCEPTANCE_PREFIX}-dns" duration="${FAULT_DURATION_SECONDS:-15}" owner
  [[ "$duration" =~ ^([5-9]|[1-5][0-9]|60)$ ]] || { e2e_die 'FAULT_DURATION_SECONDS must be between 5 and 60'; return 1; }
  ledger_record dnschaos "$name" "$TENANT_NAMESPACE" "$ACCEPTANCE_JOB_ID" || return
  kube apply --namespace "$TENANT_NAMESPACE" -f - <<EOF
apiVersion: chaos-mesh.org/v1alpha1
kind: DNSChaos
metadata:
  name: ${name}
  labels:
    ${ACCEPTANCE_LABEL_KEY}: ${ACCEPTANCE_JOB_ID}
    ${ACCEPTANCE_TENANT_LABEL_KEY}: ${ACCEPTANCE_TENANT_ID}
spec:
  action: error
  mode: all
  duration: "${duration}s"
  patterns: ["*"]
  selector:
    namespaces: ["${TENANT_NAMESPACE}"]
    labelSelectors:
      ${ACCEPTANCE_LABEL_KEY}: ${ACCEPTANCE_JOB_ID}
      ${ACCEPTANCE_TENANT_LABEL_KEY}: ${ACCEPTANCE_TENANT_ID}
EOF
  sleep "$duration"
  owner="$(kube get dnschaos.chaos-mesh.org "$name" --namespace "$TENANT_NAMESPACE" -o 'jsonpath={.metadata.labels.platform_job_id}')" || return
  [[ "$owner" == "$ACCEPTANCE_JOB_ID" ]] || { e2e_die 'DNS fault ownership changed before recovery'; return 1; }
  kube delete dnschaos.chaos-mesh.org "$name" --namespace "$TENANT_NAMESPACE" --wait=true
}

apply_tos_transient() {
  local name="${ACCEPTANCE_PREFIX}-tos" duration="${FAULT_DURATION_SECONDS:-15}" owner
  [[ "$duration" =~ ^([5-9]|[1-5][0-9]|60)$ ]] || { e2e_die 'FAULT_DURATION_SECONDS must be between 5 and 60'; return 1; }
  [[ "${TOS_FAULT_HOST:-}" =~ ^[a-z0-9]([-a-z0-9.]*[a-z0-9])$ && "$TOS_FAULT_HOST" == *.* ]] || { e2e_die 'TOS_FAULT_HOST must be one concrete lowercase DNS host'; return 1; }
  ledger_record networkchaos "$name" "$TENANT_NAMESPACE" "$ACCEPTANCE_JOB_ID" || return
  kube apply --namespace "$TENANT_NAMESPACE" -f - <<EOF
apiVersion: chaos-mesh.org/v1alpha1
kind: NetworkChaos
metadata:
  name: ${name}
  labels:
    ${ACCEPTANCE_LABEL_KEY}: ${ACCEPTANCE_JOB_ID}
    ${ACCEPTANCE_TENANT_LABEL_KEY}: ${ACCEPTANCE_TENANT_ID}
spec:
  action: loss
  mode: all
  direction: to
  duration: "${duration}s"
  loss:
    loss: "100"
    correlation: "0"
  externalTargets: ["${TOS_FAULT_HOST}"]
  selector:
    namespaces: ["${TENANT_NAMESPACE}"]
    labelSelectors:
      ${ACCEPTANCE_LABEL_KEY}: ${ACCEPTANCE_JOB_ID}
      ${ACCEPTANCE_TENANT_LABEL_KEY}: ${ACCEPTANCE_TENANT_ID}
EOF
  sleep "$duration"
  owner="$(kube get networkchaos.chaos-mesh.org "$name" --namespace "$TENANT_NAMESPACE" -o 'jsonpath={.metadata.labels.platform_job_id}')" || return
  [[ "$owner" == "$ACCEPTANCE_JOB_ID" ]] || { e2e_die 'TOS fault ownership changed before recovery'; return 1; }
  kube delete networkchaos.chaos-mesh.org "$name" --namespace "$TENANT_NAMESPACE" --wait=true
}

gpu_node_restart() {
  local pod node unrelated restart_bin
  pod="$(select_one_pod "$worker_selector")" || return
  verify_pod_ownership "$pod" || return
  node="$(kube get pod "$pod" --namespace "$TENANT_NAMESPACE" -o jsonpath='{.spec.nodeName}')" || return
  [[ "$node" == "$TARGET_GPU_NODE" ]] || { e2e_die 'selected acceptance worker is not on the explicitly confirmed GPU node'; return 1; }
  unrelated="$(kube get pods --all-namespaces --field-selector "spec.nodeName=${node}" -o json | jq --arg job "$ACCEPTANCE_JOB_ID" '[.items[] | select(.metadata.labels.platform_job_id != $job) | select(any((([.spec.containers[]?, .spec.initContainers[]?, .spec.ephemeralContainers[]?][] | [(.resources.requests // {}), (.resources.limits // {})] | add | to_entries[]?)); .key == "nvidia.com/gpu" and (.value | tonumber? // 0) > 0))] | length')" || return
  [[ "$unrelated" -eq 0 ]] || { e2e_die 'refusing node restart: unrelated GPU workload is present'; return 1; }
  restart_bin="${GPU_NODE_RESTART_BIN:-}"
  [[ "$restart_bin" == /* && -x "$restart_bin" && ! -L "$restart_bin" ]] || { e2e_die 'GPU_NODE_RESTART_BIN must be an explicit trusted executable path'; return 1; }
  ledger_record node-fault "$node" "$TENANT_NAMESPACE" "$ACCEPTANCE_JOB_ID" || return
  bounded_command "$restart_bin" --node "$node" --job-id "$ACCEPTANCE_JOB_ID" --namespace "$TENANT_NAMESPACE"
}

driver_head_failure() {
  local pod
  pod="$(select_one_pod "$head_selector")" || return
  verify_pod_ownership "$pod" || return
  ledger_record pod "$pod" "$TENANT_NAMESPACE" "$ACCEPTANCE_JOB_ID" || return
  kube delete pod "$pod" --namespace "$TENANT_NAMESPACE" --wait=false
}

main() {
  if [[ "${ALLOW_DESTRUCTIVE_FAULT_TESTS:-0}" == 1 && "${ACCEPTANCE_PREFIX+x}" != x ]]; then
    e2e_die 'destructive faults require an explicit acceptance prefix'
    return 1
  fi
  acceptance_setup || return
  printf 'ACCEPTANCE_PREFIX=%s\n' "$ACCEPTANCE_PREFIX"
  if [[ "${ALLOW_DESTRUCTIVE_FAULT_TESTS:-0}" != 1 ]]; then
    echo 'DRY_RUN destructive fault harness made no changes'
    printf 'AVAILABLE_FAULTS=%s\n' "${fault_cases[*]}"
    return 0
  fi

  require_fault_identity || return
  if ! e2e_live_enabled; then
    e2e_die 'authorized destructive faults still require explicit live mode; no action taken'
    return 1
  fi
  require_fault_live_configuration || return
  verify_acceptance_job || return
  if [[ -e "$CLEANUP_LEDGER" ]]; then
    ledger_resume || return
  else
    ledger_init || return
  fi
  case "$FAULT_CASE" in
    worker-process-exit) worker_process_exit || return ;;
    worker-pod-delete) worker_pod_delete || return ;;
    dns-transient) apply_dns_transient || return ;;
    tos-transient) apply_tos_transient || return ;;
    gpu-node-restart) gpu_node_restart || return ;;
    driver-head-failure) driver_head_failure || return ;;
  esac
  wait_for_job "$ACCEPTANCE_JOB_ID" >/dev/null || return
  printf 'PASS fault=%s job=%s; cleanup is never automatic for fault tests\n' "$FAULT_CASE" "$ACCEPTANCE_JOB_ID"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
