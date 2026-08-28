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
readonly residual_resources=(services persistentvolumeclaims rayclusters.ray.io workloads.kueue.x-k8s.io)
readonly tracked_resources=(services persistentvolumeclaims rayclusters.ray.io workloads.kueue.x-k8s.io pods jobs.batch)
readonly gpuallocations_endpoint='/api/v1/gpu-allocations'
# Child recovery is submitted by the shared spk-rayjob --resume-from-job path.

origin_for_flow() {
  case "$1" in
    portal) printf 'portal\n' ;;
    spk-rayjob) printf 'api\n' ;;
    native-ray) printf 'ray-cli\n' ;;
    *) e2e_die "unknown flow: $1" ;;
  esac
}

require_job_id() {
  [[ "${1:-}" =~ ^job-[0-9a-f]{24}$ ]] || e2e_die 'expected a persisted platform job ID'
}

verify_managed_surfaces() {
  local job_id="$1" expected_workers="$2" performance logs checkpoints mlflow dashboard checkpoint_id
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
  [[ "$checkpoint_id" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || e2e_die 'checkpoint ID is unsafe'
  printf '%s\n' "$checkpoint_id"
}

discover_owned_resources() {
  local job_id="$1" namespace="$2" resource kind item resource_json
  require_job_id "$job_id"
  validate_tenant_namespace "$namespace"
  for resource in "${tracked_resources[@]}"; do
    case "$resource" in
      services) kind=service ;;
      persistentvolumeclaims) kind=pvc ;;
      rayclusters.ray.io) kind=raycluster ;;
      workloads.kueue.x-k8s.io) kind=workload ;;
      pods) kind=pod ;;
      jobs.batch) kind=k8sjob ;;
      *) e2e_die "unsupported resource discovery kind: $resource"; return 1 ;;
    esac
    if ! resource_json="$(kube get "$resource" --namespace "$namespace" --selector "${ACCEPTANCE_LABEL_KEY}=${job_id}" -o json)"; then
      e2e_die "failed to discover owned $resource resources"
      return 1
    fi
    while IFS= read -r item; do
      [[ -n "$item" ]] || continue
      ledger_record "$kind" "$item" "$namespace" "$job_id"
    done < <(jq -r '.items[].metadata.name' <<<"$resource_json")
  done
}

wait_for_ttl_cleanup() {
  local job_id="$1" namespace="$2" deadline resource count allocations
  require_job_id "$job_id"
  validate_tenant_namespace "$namespace"
  deadline=$(( $(date +%s) + successTtlSeconds + E2E_TIMEOUT_SECONDS ))
  while [[ "$(date +%s)" -lt "$deadline" ]]; do
    count=0
    for resource in "${residual_resources[@]}"; do
      count=$(( count + $(kube get "$resource" --namespace "$namespace" --selector "${ACCEPTANCE_LABEL_KEY}=${job_id}" -o json | jq '.items | length') ))
    done
    allocations="$(portal_api_request GET "$gpuallocations_endpoint")"
    count=$(( count + $(jq --arg id "$job_id" '[.data[] | select(.id == $id)] | length' <<<"$allocations") ))
    if [[ "$count" -eq 0 ]]; then
      printf 'TTL_CLEAN job=%s resources=Service,PVC,RayCluster,GPU,KueueWorkload\n' "$job_id"
      return 0
    fi
    sleep "$E2E_POLL_SECONDS"
  done
  e2e_die "successTtlSeconds elapsed with owned resources remaining for $job_id"
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
  ledger_init

  while IFS=$'\t' read -r topology workers gpus mode; do
    for flow in "${submission_flows[@]}"; do
      local name job_id detail expected_workers checkpoint_id child_id child_detail namespace
      name="${ACCEPTANCE_PREFIX}-m-${topology}-${flow//-}"
      job_id="$(submit_acceptance_job "$flow" "$name" "$expected_engine" "$workers" "$gpus" "$mode")"
      require_job_id "$job_id"
      ledger_record job "$job_id" "$TENANT_NAMESPACE" "$job_id"
      ledger_record gpuallocation "$job_id" "$TENANT_NAMESPACE" "$job_id"
      detail="$(wait_for_job "$job_id")"
      verify_persisted_job "$detail" "$expected_engine" "$expected_ray_version" "$(origin_for_flow "$flow")" "$workers" "$gpus"
      verify_acceptance_identity "$detail" "$flow" "$name"
      record_persisted_resources "$detail"
      namespace="$(jq -er '(.data // .).kubernetesNamespace' <<<"$detail")"
      expected_workers=$(( workers * gpus ))
      if [[ "$topology" == 2x8gpu ]]; then expected_workers=16; fi
      checkpoint_id="$(verify_managed_surfaces "$job_id" "$expected_workers")"
      discover_owned_resources "$job_id" "$namespace"

      child_id="$(submit_spk_job "${name}-resume" "$expected_engine" "$workers" "$gpus" "$mode" "$job_id")"
      require_job_id "$child_id"
      ledger_record job "$child_id" "$TENANT_NAMESPACE" "$child_id"
      ledger_record gpuallocation "$child_id" "$TENANT_NAMESPACE" "$child_id"
      child_detail="$(wait_for_job "$child_id")"
      verify_persisted_job "$child_detail" "$expected_engine" "$expected_ray_version" api "$workers" "$gpus"
      verify_acceptance_identity "$child_detail" spk-rayjob "${name}-resume"
      verify_resume_provenance "$child_detail" "$job_id" "$checkpoint_id"
      record_persisted_resources "$child_detail"
      verify_managed_surfaces "$child_id" "$expected_workers" >/dev/null
      discover_owned_resources "$child_id" "$namespace"
      wait_for_ttl_cleanup "$job_id" "$namespace"
      wait_for_ttl_cleanup "$child_id" "$namespace"
      printf 'PASS topology=%s flow=%s parent=%s child=%s\n' "$topology" "$flow" "$job_id" "$child_id"
    done
  done <<<"$managed_matrix"
  ledger_cleanup
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
