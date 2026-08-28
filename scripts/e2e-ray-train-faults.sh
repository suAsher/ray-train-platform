#!/usr/bin/env bash
set -euo pipefail

readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/e2e-training.sh
source "${script_dir}/e2e-training.sh"

readonly fault_cases=(worker-process-exit worker-pod-delete dns-transient tos-transient gpu-node-restart driver-head-failure)
readonly worker_selector='ray.io/node-type=worker'
readonly head_selector='ray.io/node-type=head'

require_fault_identity() {
  [[ "${ALLOW_DESTRUCTIVE_FAULT_TESTS:-0}" == 1 ]] || e2e_die 'fault tests require ALLOW_DESTRUCTIVE_FAULT_TESTS=1'
  [[ "${ACCEPTANCE_JOB_ID:-}" =~ ^job-[0-9a-f]{24}$ ]] || e2e_die 'ACCEPTANCE_JOB_ID must be one dedicated persisted acceptance job ID'
  [[ "${ACCEPTANCE_LABEL_VALUE:-}" == "$ACCEPTANCE_JOB_ID" ]] || e2e_die 'ACCEPTANCE_LABEL_VALUE must exactly match ACCEPTANCE_JOB_ID'
  [[ "${FAULT_CASE:-}" =~ ^(worker-process-exit|worker-pod-delete|dns-transient|tos-transient|gpu-node-restart|driver-head-failure)$ ]] || e2e_die 'FAULT_CASE must select exactly one supported fault'
  if [[ "$FAULT_CASE" == gpu-node-restart ]]; then
    [[ "${TARGET_GPU_NODE:-}" =~ ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$ ]] || e2e_die 'TARGET_GPU_NODE must be one exact node name'
    [[ "${CONFIRM_GPU_NODE_RESTART:-}" == "$TARGET_GPU_NODE" ]] || e2e_die 'CONFIRM_GPU_NODE_RESTART must exactly match TARGET_GPU_NODE'
  fi
}

require_fault_live_configuration() {
  e2e_live_enabled || e2e_die 'destructive faults require RUN_RAY_TRAIN_E2E=1 and DRY_RUN=0'
  validate_tenant_namespace "${TENANT_NAMESPACE:-}"
  [[ "${API_URL:-}" =~ ^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?/?$ ]] || e2e_die 'API_URL must be a concrete HTTPS origin'
  [[ -f "${SPK_RAYJOB_CONFIG:-}" && ! -L "${SPK_RAYJOB_CONFIG:-}" ]] || e2e_die 'SPK_RAYJOB_CONFIG must be a regular file'
  command -v kubectl >/dev/null || e2e_die 'kubectl is required'
  command -v jq >/dev/null || e2e_die 'jq is required'
  command -v perl >/dev/null || e2e_die 'perl is required for bounded subprocesses'
}

exact_job_selector() {
  require_fault_identity
  printf '%s=%s\n' "$ACCEPTANCE_LABEL_KEY" "$ACCEPTANCE_JOB_ID"
}

verify_acceptance_job() {
  local detail namespace engine name
  detail="$(job_status_json "$ACCEPTANCE_JOB_ID")"
  namespace="$(jq -er '(.data // .).kubernetesNamespace' <<<"$detail")"
  engine="$(jq -er '(.data // .).spec.trainingEngine' <<<"$detail")"
  name="$(jq -er '(.data // .).spec.name' <<<"$detail")"
  [[ "$namespace" == "$TENANT_NAMESPACE" ]] || e2e_die 'acceptance job is outside the explicit tenant namespace'
  [[ "$engine" == ray-train ]] || e2e_die 'destructive faults require a managed Ray Train acceptance job'
  [[ "$name" == "${ACCEPTANCE_PREFIX}-"* ]] || e2e_die 'acceptance job name does not match ACCEPTANCE_PREFIX'
}

select_one_pod() {
  local node_type_selector="$1" selector response count pod
  selector="$(exact_job_selector),${node_type_selector}"
  [[ "$selector" != *'*'* && "$selector" != *','','* ]] || e2e_die 'refusing an empty or broad selector'
  response="$(kube get pods --namespace "$TENANT_NAMESPACE" --selector "$selector" -o json)"
  count="$(jq '.items | length' <<<"$response")"
  [[ "$count" -gt 0 ]] || e2e_die "no exact acceptance pod matched $selector"
  pod="$(jq -er '.items | sort_by(.metadata.name) | first | .metadata.name' <<<"$response")"
  [[ "$pod" =~ ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$ ]] || e2e_die 'selected pod name is unsafe'
  printf '%s\n' "$pod"
}

verify_pod_ownership() {
  local pod="$1" owner
  owner="$(kube get pod "$pod" --namespace "$TENANT_NAMESPACE" -o "jsonpath={.metadata.labels.ray\\.io/job-id}")"
  [[ "$owner" == "$ACCEPTANCE_JOB_ID" ]] || e2e_die "pod $pod lost its exact acceptance ownership label"
}

worker_process_exit() {
  local pod
  pod="$(select_one_pod "$worker_selector")"
  verify_pod_ownership "$pod"
  ledger_record pod "$pod" "$TENANT_NAMESPACE" "$ACCEPTANCE_JOB_ID"
  kube exec "$pod" --namespace "$TENANT_NAMESPACE" -- sh -ceu 'pid="$(pgrep -f "ray::.*TrainWorker" | head -n 1)"; test -n "$pid"; kill -TERM "$pid"'
}

worker_pod_delete() {
  local pod
  pod="$(select_one_pod "$worker_selector")"
  verify_pod_ownership "$pod"
  ledger_record pod "$pod" "$TENANT_NAMESPACE" "$ACCEPTANCE_JOB_ID"
  kube delete pod "$pod" --namespace "$TENANT_NAMESPACE" --wait=false
}

apply_dns_transient() {
  local name="${ACCEPTANCE_PREFIX}-dns" duration="${FAULT_DURATION_SECONDS:-15}" owner
  [[ "$duration" =~ ^([5-9]|[1-5][0-9]|60)$ ]] || e2e_die 'FAULT_DURATION_SECONDS must be between 5 and 60'
  ledger_record dnschaos "$name" "$TENANT_NAMESPACE" "$ACCEPTANCE_JOB_ID"
  kube apply --namespace "$TENANT_NAMESPACE" -f - <<EOF
apiVersion: chaos-mesh.org/v1alpha1
kind: DNSChaos
metadata:
  name: ${name}
  labels:
    ${ACCEPTANCE_LABEL_KEY}: ${ACCEPTANCE_JOB_ID}
spec:
  action: error
  mode: all
  duration: "${duration}s"
  patterns: ["*"]
  selector:
    namespaces: ["${TENANT_NAMESPACE}"]
    labelSelectors:
      ${ACCEPTANCE_LABEL_KEY}: ${ACCEPTANCE_JOB_ID}
EOF
  sleep "$duration"
  owner="$(kube get dnschaos.chaos-mesh.org "$name" --namespace "$TENANT_NAMESPACE" -o 'jsonpath={.metadata.labels.ray\.io/job-id}')"
  [[ "$owner" == "$ACCEPTANCE_JOB_ID" ]] || e2e_die 'DNS fault ownership changed before recovery'
  kube delete dnschaos.chaos-mesh.org "$name" --namespace "$TENANT_NAMESPACE" --wait=true
}

apply_tos_transient() {
  local name="${ACCEPTANCE_PREFIX}-tos" duration="${FAULT_DURATION_SECONDS:-15}" owner
  [[ "$duration" =~ ^([5-9]|[1-5][0-9]|60)$ ]] || e2e_die 'FAULT_DURATION_SECONDS must be between 5 and 60'
  [[ "${TOS_FAULT_HOST:-}" =~ ^[a-z0-9]([-a-z0-9.]*[a-z0-9])$ && "$TOS_FAULT_HOST" == *.* ]] || e2e_die 'TOS_FAULT_HOST must be one concrete lowercase DNS host'
  ledger_record networkchaos "$name" "$TENANT_NAMESPACE" "$ACCEPTANCE_JOB_ID"
  kube apply --namespace "$TENANT_NAMESPACE" -f - <<EOF
apiVersion: chaos-mesh.org/v1alpha1
kind: NetworkChaos
metadata:
  name: ${name}
  labels:
    ${ACCEPTANCE_LABEL_KEY}: ${ACCEPTANCE_JOB_ID}
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
EOF
  sleep "$duration"
  owner="$(kube get networkchaos.chaos-mesh.org "$name" --namespace "$TENANT_NAMESPACE" -o 'jsonpath={.metadata.labels.ray\.io/job-id}')"
  [[ "$owner" == "$ACCEPTANCE_JOB_ID" ]] || e2e_die 'TOS fault ownership changed before recovery'
  kube delete networkchaos.chaos-mesh.org "$name" --namespace "$TENANT_NAMESPACE" --wait=true
}

gpu_node_restart() {
  local pod node unrelated restart_bin
  pod="$(select_one_pod "$worker_selector")"
  verify_pod_ownership "$pod"
  node="$(kube get pod "$pod" --namespace "$TENANT_NAMESPACE" -o jsonpath='{.spec.nodeName}')"
  [[ "$node" == "$TARGET_GPU_NODE" ]] || e2e_die 'selected acceptance worker is not on the explicitly confirmed GPU node'
  unrelated="$(kube get pods --all-namespaces --field-selector "spec.nodeName=${node}" -o json | jq --arg job "$ACCEPTANCE_JOB_ID" '[.items[] | select(.metadata.labels["ray.io/job-id"] != $job) | select(any((([.spec.containers[]?, .spec.initContainers[]?, .spec.ephemeralContainers[]?][] | [(.resources.requests // {}), (.resources.limits // {})] | add | to_entries[]?)); .key == "nvidia.com/gpu" and (.value | tonumber? // 0) > 0))] | length')"
  [[ "$unrelated" -eq 0 ]] || e2e_die 'refusing node restart: unrelated GPU workload is present'
  restart_bin="${GPU_NODE_RESTART_BIN:-}"
  [[ "$restart_bin" == /* && -x "$restart_bin" && ! -L "$restart_bin" ]] || e2e_die 'GPU_NODE_RESTART_BIN must be an explicit trusted executable path'
  ledger_record node-fault "$node" "$TENANT_NAMESPACE" "$ACCEPTANCE_JOB_ID"
  bounded_command "$restart_bin" --node "$node" --job-id "$ACCEPTANCE_JOB_ID" --namespace "$TENANT_NAMESPACE"
}

driver_head_failure() {
  local pod
  pod="$(select_one_pod "$head_selector")"
  verify_pod_ownership "$pod"
  ledger_record pod "$pod" "$TENANT_NAMESPACE" "$ACCEPTANCE_JOB_ID"
  kube delete pod "$pod" --namespace "$TENANT_NAMESPACE" --wait=false
}

main() {
  if [[ "${ALLOW_DESTRUCTIVE_FAULT_TESTS:-0}" == 1 && "${ACCEPTANCE_PREFIX+x}" != x ]]; then
    e2e_die 'destructive faults require an explicit acceptance prefix'
  fi
  acceptance_setup
  printf 'ACCEPTANCE_PREFIX=%s\n' "$ACCEPTANCE_PREFIX"
  if [[ "${ALLOW_DESTRUCTIVE_FAULT_TESTS:-0}" != 1 ]]; then
    echo 'DRY_RUN destructive fault harness made no changes'
    printf 'AVAILABLE_FAULTS=%s\n' "${fault_cases[*]}"
    return 0
  fi

  require_fault_identity
  if ! e2e_live_enabled; then
    e2e_die 'authorized destructive faults still require explicit live mode; no action taken'
  fi
  require_fault_live_configuration
  verify_acceptance_job
  if [[ -e "$CLEANUP_LEDGER" ]]; then
    ledger_resume
  else
    ledger_init
  fi
  case "$FAULT_CASE" in
    worker-process-exit) worker_process_exit ;;
    worker-pod-delete) worker_pod_delete ;;
    dns-transient) apply_dns_transient ;;
    tos-transient) apply_tos_transient ;;
    gpu-node-restart) gpu_node_restart ;;
    driver-head-failure) driver_head_failure ;;
  esac
  wait_for_job "$ACCEPTANCE_JOB_ID" >/dev/null
  printf 'PASS fault=%s job=%s; cleanup is never automatic for fault tests\n' "$FAULT_CASE" "$ACCEPTANCE_JOB_ID"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
