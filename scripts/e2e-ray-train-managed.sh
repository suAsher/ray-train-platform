#!/usr/bin/env bash
set -euo pipefail

readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/e2e-training.sh
source "${script_dir}/e2e-training.sh"

readonly expected_engine='ray-train'
readonly expected_ray_version='2.56.1'
readonly successTtlSeconds=60
readonly managed_matrix=$'1gpu\t1\t1\tsingle_gpu\n8gpu\t1\t8\ttorchrun\n2x8gpu\t2\t8\tray_train'
readonly submission_flows=(portal spk-rayjob native-ray)
readonly gpuallocations_endpoint='/api/v1/gpu-allocations'
# Child recovery is submitted by the shared spk-rayjob --resume-from-job path.

origin_for_flow() {
  case "$1" in
    portal) printf 'portal\n' ;;
    spk-rayjob) printf 'ray-cli\n' ;;
    native-ray) printf 'ray-cli\n' ;;
    *) e2e_die "unknown flow: $1" ;;
  esac
}

require_job_id() {
  [[ "${1:-}" =~ ^job-[0-9a-f]{24}$ ]] || e2e_die 'expected a persisted platform job ID'
}

verify_managed_surfaces() {
  local job_id="$1" expected_workers="$2" performance logs checkpoints mlflow dashboard checkpoint_id checkpoint_step
  require_job_id "$job_id"

  performance="$(portal_api_request GET "/api/v1/jobs/${job_id}/training-performance?window=1h")"
  jq -e --argjson expected "$expected_workers" '.data.workers | type == "array" and length == $expected' <<<"$performance" >/dev/null || e2e_die 'training-performance did not expose the exact Ray Train worker count'

  logs="$(portal_api_request GET "/api/v1/jobs/${job_id}/logs?limit=200&direction=backward")"
  jq -e '(.data.items // .data.lines // []) | type == "array" and length > 0' <<<"$logs" >/dev/null || e2e_die 'job /logs contained no training output'

  mlflow="$(portal_api_request POST '/api/v1/mlflow-dashboard-access')"
  jq -e '.data.url | type == "string" and startswith("/api/")' <<<"$mlflow" >/dev/null || e2e_die 'MLflow access was not issued'

  dashboard="$(portal_api_request POST "/api/v1/jobs/${job_id}/dashboard-access")"
  jq -e --arg job "$job_id" '.data.url | type == "string" and contains(("/jobs/" + $job + "/dashboard/"))' <<<"$dashboard" >/dev/null || e2e_die 'job dashboard access was not issued for the persisted job'

  checkpoints="$(portal_api_request GET "/api/v1/jobs/${job_id}/checkpoints")"
  checkpoint_id="$(jq -er --arg job "$job_id" '.data as $data | select($data.jobId == $job) | [$data.items[] | select(.complete == true and (.manifestSha256 | test("^[0-9a-f]{64}$")))] | sort_by(.step) | last | .id' <<<"$checkpoints")" || e2e_die 'no complete checkpoint manifest was published'
  checkpoint_step="$(jq -er --arg id "$checkpoint_id" '.data.items[] | select(.id == $id and .complete == true) | .step' <<<"$checkpoints")" || e2e_die 'complete checkpoint has no persisted step'
  [[ "$checkpoint_id" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || e2e_die 'checkpoint ID is unsafe'
  [[ "$checkpoint_step" =~ ^[0-9]+$ ]] || e2e_die 'checkpoint step is unsafe'
  printf '%s\t%s\n' "$checkpoint_id" "$checkpoint_step"
}

_validated_list_json() {
  local description="$1"
  shift
  local payload
  payload="$(kube "$@")" || {
    e2e_die "failed to discover owned $description resources"
    return 1
  }
  jq -e 'type == "object" and (.items | type == "array")' <<<"$payload" >/dev/null || {
    e2e_die "owned $description discovery returned malformed JSON"
    return 1
  }
  printf '%s\n' "$payload"
}

_validated_exact_json() {
  local description="$1"
  shift
  local payload
  payload="$(kube "$@")" || {
    e2e_die "failed to read exact owned $description resource"
    return 1
  }
  if [[ -z "$payload" ]]; then
    printf '{}\n'
    return 0
  fi
  jq -e 'type == "object" and (.metadata | type == "object")' <<<"$payload" >/dev/null || {
    e2e_die "exact owned $description lookup returned malformed JSON"
    return 1
  }
  printf '%s\n' "$payload"
}

_owned_resources() {
  local detail="$1" job_id tenant_id namespace ray_job ray_job_uid ray_cluster
  local cluster_json cluster_uid pods_json pod_rows pod_name pod_uid volume_name pvc_name pvc_json
  local services_json workloads_json jobs_json rows
  job_id="$(jq -er '(.data // .).id' <<<"$detail")" || return 1
  tenant_id="$(jq -er '(.data // .).tenantId' <<<"$detail")" || return 1
  namespace="$(jq -er '(.data // .).kubernetesNamespace' <<<"$detail")" || return 1
  ray_job="$(jq -er '(.data // .).rayJobName' <<<"$detail")" || return 1
  ray_job_uid="$(jq -er '(.data // .).rayJobUid' <<<"$detail")" || return 1
  ray_cluster="$(jq -er '(.data // .).rayClusterName' <<<"$detail")" || return 1
  require_job_id "$job_id" || return 1
  [[ "$tenant_id" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || { e2e_die 'persisted tenant identity is unsafe'; return 1; }
  validate_tenant_namespace "$namespace" || return 1
  [[ "$ray_job" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ && "$ray_cluster" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || { e2e_die 'persisted Ray identity is unsafe'; return 1; }
  [[ "$ray_job_uid" =~ ^[A-Za-z0-9][A-Za-z0-9._:-]{0,252}$ ]] || { e2e_die 'persisted RayJob UID is unsafe'; return 1; }

  cluster_json="$(_validated_exact_json RayCluster get rayclusters.ray.io "$ray_cluster" --namespace "$namespace" --ignore-not-found -o json)" || return 1
  cluster_uid=''
  if jq -e 'has("metadata")' <<<"$cluster_json" >/dev/null; then
    jq -e --arg job "$job_id" --arg tenant "$tenant_id" --arg rayjob "$ray_job" --arg uid "$ray_job_uid" '(.metadata.name | type == "string") and (((.metadata.labels.platform_job_id == $job) and (.metadata.labels.platform_tenant_id == $tenant)) or any(.metadata.ownerReferences[]?; .kind == "RayJob" and .name == $rayjob and .uid == $uid))' <<<"$cluster_json" >/dev/null || {
      e2e_die 'persisted RayCluster identity does not own the discovered cluster'
      return 1
    }
    cluster_uid="$(jq -er '.metadata.uid' <<<"$cluster_json")" || return 1
    printf 'raycluster\t%s\n' "$ray_cluster"
  fi

  pods_json="$(_validated_list_json Pods get pods --namespace "$namespace" --selector "${ACCEPTANCE_LABEL_KEY}=${job_id},${ACCEPTANCE_TENANT_LABEL_KEY}=${tenant_id}" -o json)" || return 1
  jq -e --arg job "$job_id" --arg tenant "$tenant_id" 'all(.items[]; .metadata.labels.platform_job_id == $job and .metadata.labels.platform_tenant_id == $tenant and (.metadata.name | type == "string") and (.metadata.uid | type == "string"))' <<<"$pods_json" >/dev/null || {
    e2e_die 'pod discovery returned a false-positive ownership match'
    return 1
  }
  rows="$(jq -r '.items[] | [.metadata.name,.metadata.uid] | @tsv' <<<"$pods_json")" || return 1
  while IFS=$'\t' read -r pod_name pod_uid; do
    [[ -n "$pod_name" ]] || continue
    printf 'pod\t%s\n' "$pod_name"
  done <<<"$rows"
  pod_rows="$(jq -r '.items[] as $pod | $pod.spec.volumes[]? | select(has("ephemeral")) | [$pod.metadata.name,$pod.metadata.uid,.name] | @tsv' <<<"$pods_json")" || return 1
  while IFS=$'\t' read -r pod_name pod_uid volume_name; do
    [[ -n "$pod_name" ]] || continue
    pvc_name="${pod_name}-${volume_name}"
    pvc_json="$(_validated_exact_json PVC get persistentvolumeclaims "$pvc_name" --namespace "$namespace" --ignore-not-found -o json)" || return 1
    if jq -e 'has("metadata")' <<<"$pvc_json" >/dev/null; then
      jq -e --arg pod "$pod_name" --arg uid "$pod_uid" 'any(.metadata.ownerReferences[]?; .kind == "Pod" and .name == $pod and .uid == $uid)' <<<"$pvc_json" >/dev/null || {
        e2e_die 'ephemeral PVC owner does not match its exact acceptance Pod'
        return 1
      }
      printf 'pvc\t%s\n' "$pvc_name"
    fi
  done <<<"$pod_rows"

  services_json="$(_validated_list_json Services get services --namespace "$namespace" --selector "ray.io/cluster=${ray_cluster}" -o json)" || return 1
  if [[ "$(jq '.items | length' <<<"$services_json")" -gt 0 && -z "$cluster_uid" ]]; then
    e2e_die 'Service exists without the exact persisted RayCluster owner'
    return 1
  fi
  jq -e --arg cluster "$ray_cluster" --arg uid "$cluster_uid" 'all(.items[]; any(.metadata.ownerReferences[]?; .kind == "RayCluster" and .name == $cluster and .uid == $uid))' <<<"$services_json" >/dev/null || {
    e2e_die 'Service discovery returned an exporter or foreign cluster false positive'
    return 1
  }
  rows="$(jq -r '.items[].metadata.name' <<<"$services_json")" || return 1
  while IFS= read -r pod_name; do [[ -z "$pod_name" ]] || printf 'service\t%s\n' "$pod_name"; done <<<"$rows"

  workloads_json="$(_validated_list_json Workloads get workloads.kueue.x-k8s.io --namespace "$namespace" --selector "kueue.x-k8s.io/job-uid=${ray_job_uid}" -o json)" || return 1
  jq -e --arg rayjob "$ray_job" --arg uid "$ray_job_uid" 'all(.items[]; any(.metadata.ownerReferences[]?; .kind == "RayJob" and .name == $rayjob and .uid == $uid))' <<<"$workloads_json" >/dev/null || {
    e2e_die 'Workload discovery returned a foreign RayJob owner'
    return 1
  }
  rows="$(jq -r '.items[].metadata.name' <<<"$workloads_json")" || return 1
  while IFS= read -r pod_name; do [[ -z "$pod_name" ]] || printf 'workload\t%s\n' "$pod_name"; done <<<"$rows"

  jobs_json="$(_validated_list_json submitter-Jobs get jobs.batch --namespace "$namespace" --selector "ray.io/originated-from-cr-name=${ray_job}" -o json)" || return 1
  jq -e --arg rayjob "$ray_job" --arg uid "$ray_job_uid" 'all(.items[]; any(.metadata.ownerReferences[]?; .kind == "RayJob" and .name == $rayjob and .uid == $uid))' <<<"$jobs_json" >/dev/null || {
    e2e_die 'submitter Job discovery returned a foreign RayJob owner'
    return 1
  }
  rows="$(jq -r '.items[].metadata.name' <<<"$jobs_json")" || return 1
  while IFS= read -r pod_name; do [[ -z "$pod_name" ]] || printf 'k8sjob\t%s\n' "$pod_name"; done <<<"$rows"
}

discover_owned_resources() {
  local detail="$1" job_id namespace owned kind item
  job_id="$(jq -er '(.data // .).id' <<<"$detail")" || return 1
  namespace="$(jq -er '(.data // .).kubernetesNamespace' <<<"$detail")" || return 1
  owned="$(_owned_resources "$detail")" || return 1
  while IFS=$'\t' read -r kind item; do
    [[ -n "$kind" ]] || continue
    ledger_record "$kind" "$item" "$namespace" "$job_id"
  done <<<"$owned"
}

wait_for_ttl_cleanup() {
  local detail="$1" job_id namespace deadline count allocations allocation_count owned
  job_id="$(jq -er '(.data // .).id' <<<"$detail")" || return 1
  namespace="$(jq -er '(.data // .).kubernetesNamespace' <<<"$detail")" || return 1
  require_job_id "$job_id" || return 1
  validate_tenant_namespace "$namespace" || return 1
  deadline=$(( $(date +%s) + successTtlSeconds + E2E_TIMEOUT_SECONDS ))
  while [[ "$(date +%s)" -lt "$deadline" ]]; do
    owned="$(_owned_resources "$detail")" || return 1
    count="$(awk -F '\t' '$1 == "service" || $1 == "pvc" || $1 == "raycluster" || $1 == "workload" {count++} END {print count+0}' <<<"$owned")"
    allocations="$(portal_api_request GET "$gpuallocations_endpoint")" || return 1
    jq -e '.data | type == "array"' <<<"$allocations" >/dev/null || { e2e_die 'GPU allocation cleanup query returned malformed JSON'; return 1; }
    allocation_count="$(jq --arg id "$job_id" '[.data[] | select(.id == $id)] | length' <<<"$allocations")" || return 1
    count=$(( count + allocation_count ))
    if [[ "$count" -eq 0 ]]; then
      printf 'TTL_CLEAN job=%s resources=Service,PVC,RayCluster,GPU,KueueWorkload\n' "$job_id"
      return 0
    fi
    sleep "$E2E_POLL_SECONDS"
  done
  e2e_die "successTtlSeconds elapsed with owned resources remaining for $job_id"
}

verify_resume_consumption() {
  local child_job_id="$1" parent_step="$2" expected_step logs artifact content
  require_job_id "$child_job_id" || return 1
  [[ "$parent_step" =~ ^[0-9]+$ ]] || { e2e_die 'parent checkpoint step is unsafe'; return 1; }
  expected_step=$(( parent_step + 1 ))
  logs="$(portal_api_request GET "/api/v1/jobs/${child_job_id}/logs?limit=200&direction=forward")" || return 1
  jq -e --argjson step "$expected_step" '[(.data.items // .data.lines // [])[] | (.line // .message // .) | select(type == "string" and contains("RAY_TRAIN_CHECKPOINT_SMOKE rank=0 ") and contains("resumed=True")) | capture("step=(?<step>[0-9]+)") | .step | tonumber] | first == $step' <<<"$logs" >/dev/null || e2e_die 'child logs do not prove the first resumed step consumed the parent checkpoint'
  artifact="$(portal_api_request GET "/api/v1/jobs/${child_job_id}/artifacts/preview?path=ray-train-checkpoint-smoke-result.json")" || return 1
  content="$(jq -er '.data.content' <<<"$artifact")" || return 1
  jq -e --argjson parent "$parent_step" --argjson first "$expected_step" '.resumed == true and .parentStep == $parent and .firstStep == $first' <<<"$content" >/dev/null || e2e_die 'child result artifact does not prove checkpoint consumption'
}

verify_resume_provenance() {
  local child_detail="$1" parent_job_id="$2" expected_checkpoint="$3"
  jq -e --arg parent "$parent_job_id" --arg checkpoint "$expected_checkpoint" '(.data // .) as $job | $job.spec.parentJobId == $parent and $job.resumeCheckpointId == $checkpoint' <<<"$child_detail" >/dev/null || e2e_die 'child parentJobId/resumeCheckpointId provenance is incomplete'
}

main() {
  acceptance_setup
  printf 'ACCEPTANCE_PREFIX=%s\n' "$ACCEPTANCE_PREFIX"
  if ! e2e_live_enabled; then
    echo 'DRY_RUN managed Ray Train 2.56.1 acceptance matrix'
    while IFS=$'\t' read -r topology workers gpus mode; do
      for flow in "${submission_flows[@]}"; do
        expected_workers=$(( workers * gpus ))
        if [[ "$topology" == 2x8gpu ]]; then expected_workers=16; fi
        printf 'CASE topology=%s workers=%s gpusPerWorker=%s expectedWorkers=%s execution=%s flow=%s engine=%s\n' "$topology" "$workers" "$gpus" "$expected_workers" "$mode" "$flow" "$expected_engine"
      done
    done <<<"$managed_matrix"
    return 0
  fi

  require_live_configuration
  [[ -f "${PORTAL_SESSION_CONFIG:-}" ]] || e2e_die 'PORTAL_SESSION_CONFIG is required for scoped observability checks'
  [[ -n "${PORTAL_SOURCE_SNAPSHOT_ID:-}" ]] || e2e_die 'PORTAL_SOURCE_SNAPSHOT_ID is required for a real Portal submission'
  command -v kubectl >/dev/null || e2e_die 'kubectl is required for exact-label residual checks'
  command -v docker >/dev/null || e2e_die 'docker is required for the native Ray acceptance flow'
  verify_interactive_portal_session
  ledger_init

  while IFS=$'\t' read -r topology workers gpus mode; do
    for flow in "${submission_flows[@]}"; do
      local name job_id detail expected_workers checkpoint_id checkpoint_step child_id child_detail
      name="${ACCEPTANCE_PREFIX}-m-${topology}-${flow//-}"
      job_id="$(submit_acceptance_job "$flow" "$name" "$expected_engine" "$workers" "$gpus" "$mode")"
      require_job_id "$job_id"
      ledger_record job "$job_id" "$TENANT_NAMESPACE" "$job_id"
      ledger_record gpuallocation "$job_id" "$TENANT_NAMESPACE" "$job_id"
      detail="$(wait_for_job "$job_id")"
      verify_persisted_job "$detail" "$expected_engine" "$expected_ray_version" "$(origin_for_flow "$flow")" "$workers" "$gpus"
      verify_acceptance_identity "$detail" "$flow" "$name"
      record_persisted_resources "$detail"
      expected_workers=$(( workers * gpus ))
      if [[ "$topology" == 2x8gpu ]]; then expected_workers=16; fi
      IFS=$'\t' read -r checkpoint_id checkpoint_step <<<"$(verify_managed_surfaces "$job_id" "$expected_workers")"
      discover_owned_resources "$detail"

      child_id="$(submit_acceptance_job spk-rayjob "${name}-resume" "$expected_engine" "$workers" "$gpus" "$mode" "$job_id")"
      require_job_id "$child_id"
      ledger_record job "$child_id" "$TENANT_NAMESPACE" "$child_id"
      ledger_record gpuallocation "$child_id" "$TENANT_NAMESPACE" "$child_id"
      child_detail="$(wait_for_job "$child_id")"
      verify_persisted_job "$child_detail" "$expected_engine" "$expected_ray_version" ray-cli "$workers" "$gpus"
      verify_acceptance_identity "$child_detail" spk-rayjob "${name}-resume"
      verify_resume_provenance "$child_detail" "$job_id" "$checkpoint_id"
      verify_resume_consumption "$child_id" "$checkpoint_step"
      record_persisted_resources "$child_detail"
      verify_managed_surfaces "$child_id" "$expected_workers" >/dev/null
      discover_owned_resources "$child_detail"
      wait_for_ttl_cleanup "$detail"
      wait_for_ttl_cleanup "$child_detail"
      printf 'PASS topology=%s flow=%s parent=%s child=%s\n' "$topology" "$flow" "$job_id" "$child_id"
    done
  done <<<"$managed_matrix"
  ledger_cleanup
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
