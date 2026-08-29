#!/usr/bin/env bash
set -euo pipefail

readonly script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/e2e-training.sh
source "${script_dir}/e2e-training.sh"

readonly expected_engine='ray-ddp'
readonly expected_ray_version='2.56.1'
readonly PORTAL_JOBS_ENDPOINT='/api/v1/jobs'
# Persisted acceptance invariants: submissionOrigin, trainingEngine, rayVersion,
# workerReplicas, and gpusPerWorker are all checked by verify_persisted_job.
readonly runtime_matrix=$'1gpu\t1\t1\tsingle_gpu\n8gpu\t1\t8\ttorchrun\n2x8gpu\t2\t8\tray_train'
readonly submission_flows=(portal spk-rayjob native-ray)

origin_for_flow() {
  case "$1" in
    portal) printf 'portal\n' ;;
    spk-rayjob) printf 'ray-cli\n' ;;
    native-ray) printf 'ray-cli\n' ;;
    *) e2e_die "unknown flow: $1" ;;
  esac
}

main() {
  acceptance_setup || return
  printf 'ACCEPTANCE_PREFIX=%s\n' "$ACCEPTANCE_PREFIX"
  if ! e2e_live_enabled; then
    echo 'DRY_RUN runtime Ray DDP 2.56.1 acceptance matrix'
    while IFS=$'\t' read -r topology workers gpus mode; do
      for flow in "${submission_flows[@]}"; do
        printf 'CASE topology=%s workers=%s gpusPerWorker=%s execution=%s flow=%s engine=%s rayVersion=%s\n' "$topology" "$workers" "$gpus" "$mode" "$flow" "$expected_engine" "$expected_ray_version"
      done
    done <<<"$runtime_matrix"
    return 0
  fi

  require_live_configuration || return
  [[ -f "${PORTAL_SESSION_CONFIG:-}" ]] || { e2e_die 'PORTAL_SESSION_CONFIG is required; Portal cannot be emulated with a PAT'; return 1; }
  [[ -n "${PORTAL_SOURCE_SNAPSHOT_ID:-}" ]] || { e2e_die "Portal ${PORTAL_JOBS_ENDPOINT} submission requires PORTAL_SOURCE_SNAPSHOT_ID"; return 1; }
  command -v docker >/dev/null || { e2e_die 'docker is required for the native Ray acceptance flow'; return 1; }
  verify_interactive_portal_session || return
  ledger_init || return
  local name job_id detail origin
  while IFS=$'\t' read -r topology workers gpus mode; do
    for flow in "${submission_flows[@]}"; do
      name="${ACCEPTANCE_PREFIX}-r-${topology}-${flow//-}"
      job_id="$(submit_acceptance_job "$flow" "$name" "$expected_engine" "$workers" "$gpus" "$mode")" || return
      [[ "$job_id" =~ ^job-[0-9a-f]{24}$ ]] || { e2e_die "submission returned an unsafe persisted job ID: $job_id"; return 1; }
      ledger_record job "$job_id" "$TENANT_NAMESPACE" "$job_id" || return
      ledger_record gpuallocation "$job_id" "$TENANT_NAMESPACE" "$job_id" || return
      detail="$(wait_for_job "$job_id")" || return
      origin="$(origin_for_flow "$flow")" || return
      verify_persisted_job "$detail" "$expected_engine" "$expected_ray_version" "$origin" "$workers" "$gpus" || return
      verify_acceptance_identity "$detail" "$flow" "$name" || return
      record_persisted_resources "$detail" || return
      printf 'PASS topology=%s flow=%s job=%s\n' "$topology" "$flow" "$job_id"
    done
  done <<<"$runtime_matrix"
  ledger_cleanup || return
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
