#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly DEPLOY="${ROOT_DIR}/ops/mlflow/deploy.sh"
readonly README="${ROOT_DIR}/ops/mlflow/README.md"

fault_dir="$(mktemp -d)"
trap 'rm -rf "$fault_dir"' EXIT

# Reproduce the old GET-then-name-DELETE race. The fake API changes the object
# after the UID read; only a DeleteOptions UID precondition can reject it.
cleanup_job_body="$(sed -n '/^cleanup_job_instance()/,/^}/p' "$DEPLOY")"
toctou_log="${fault_dir}/job-toctou.log"
: >"$toctou_log"
if (
  NAMESPACE='mlflow-system'
  KUBECTL_CALL_LOG="$toctou_log"

  kubectl() {
    case "$*" in
      *' get job '*) printf '%s' 'old-job-uid' ;;
      *'delete --raw '*)
        local payload
        payload="$(</dev/stdin)"
        printf 'uid-delete %s %s\n' "$*" "$payload" >>"$KUBECTL_CALL_LOG"
        return 55 # The API server reports that the name now has another UID.
        ;;
      *' delete job '*)
        printf 'blind-name-delete %s\n' "$*" >>"$KUBECTL_CALL_LOG"
        return 0
        ;;
      *) return 0 ;;
    esac
  }

  eval "$cleanup_job_body"
  cleanup_job_instance mlflow-artifact-acceptance-old-run old-job-uid >/dev/null 2>&1
); then
  echo 'Job cleanup remained successful after a same-name UID replacement' >&2
  exit 1
fi
if grep -Fq 'blind-name-delete' "$toctou_log"; then
  echo 'Job cleanup must not use a TOCTOU-prone name-only delete' >&2
  exit 1
fi
grep -Fq 'uid-delete' "$toctou_log" || {
  echo 'Job cleanup must issue a raw delete with UID preconditions' >&2
  exit 1
}
grep -Fq 'old-job-uid' "$toctou_log" || {
  echo 'Job cleanup delete options must bind the created Job UID' >&2
  exit 1
}
grep -Fq 'preconditions' "$toctou_log" || {
  echo 'Job cleanup must send Kubernetes DeleteOptions preconditions' >&2
  exit 1
}

grep -Fq 'coordination.k8s.io/v1' "$DEPLOY" || {
  echo 'MLflow deploy must use a coordination.k8s.io/v1 Lease' >&2
  exit 1
}
grep -Fq 'resourceVersion' "$DEPLOY" || {
  echo 'MLflow deploy Lease updates must retain resourceVersion' >&2
  exit 1
}
if grep -Fq 'leaseDurationSeconds' "$DEPLOY"; then
  echo 'fail-closed deployment Lease must not imply an automatic expiry' >&2
  exit 1
fi
grep -Fq 'holderIdentity' "$DEPLOY"
if grep -Fq 'fromdateiso8601' "$DEPLOY"; then
  echo 'MLflow deploy must never infer that a non-empty Lease holder is safe to replace' >&2
  exit 1
fi
if grep -Eq 'delete (lease|leases)' "$DEPLOY"; then
  echo 'MLflow deploy must release its Lease by optimistic update, never delete it' >&2
  exit 1
fi
if grep -Fq 'delete job mlflow-tos-prefix-init' "$DEPLOY"; then
  echo 'legacy Job cleanup must not retain a fixed-name blind delete' >&2
  exit 1
fi
grep -Fq 'cleanup_legacy_job mlflow-tos-prefix-init' "$DEPLOY" || {
  echo 'legacy Job cleanup must resolve and bind the old Job UID' >&2
  exit 1
}
grep -Fq 'if [[ "${BASH_SOURCE[0]}" == "$0" ]]' "$DEPLOY" || {
  echo 'deploy.sh must be sourceable for fault-injection contracts' >&2
  exit 1
}
trap_line="$(grep -nF '  install_deploy_traps' "$DEPLOY" | cut -d: -f1)"
lease_line="$(grep -nF '  acquire_deploy_lease ||' "$DEPLOY" | cut -d: -f1)"
first_mutation_line="$(grep -nF '  copy_secret harbor-registry' "$DEPLOY" | cut -d: -f1)"
if ! (( trap_line < lease_line && lease_line < first_mutation_line )); then
  echo 'MLflow deploy must install release traps and acquire the Lease before shared-state mutations' >&2
  exit 1
fi
[[ "$(grep -Fc 'release_deploy_lease' "$DEPLOY")" == '2' ]] || {
  echo 'the deployment Lease must only be released by the universal exit trap' >&2
  exit 1
}

run_lease_case() {
  local scenario="$1"
  local case_dir="${fault_dir}/${scenario}"
  mkdir -p "$case_dir"

  CASE_NAME="$scenario" CASE_DIR="$case_dir" DEPLOY_PATH="$DEPLOY" MLFLOW_DEPLOY_RUN_ID='run-one' \
    bash -c '
      set -euo pipefail

      kubectl() {
        printf "%s\n" "$*" >>"${CASE_DIR}/calls.log"
        case "${CASE_NAME}: $*" in
          active:*" get lease "*)
            printf "%s\n" "{\"metadata\":{\"name\":\"mlflow-deploy\",\"resourceVersion\":\"5\"},\"spec\":{\"holderIdentity\":\"other-holder\",\"renewTime\":\"2099-01-01T00:00:00Z\",\"leaseDurationSeconds\":21600}}"
            ;;
          stale:*" get lease "*)
            printf "%s\n" "{\"apiVersion\":\"coordination.k8s.io/v1\",\"kind\":\"Lease\",\"metadata\":{\"name\":\"mlflow-deploy\",\"namespace\":\"mlflow-system\",\"resourceVersion\":\"7\"},\"spec\":{\"holderIdentity\":\"stale-holder\",\"acquireTime\":\"2000-01-01T00:00:00Z\",\"renewTime\":\"2000-01-01T00:00:00Z\",\"leaseDurationSeconds\":60,\"leaseTransitions\":2}}"
            ;;
          release-other:*" get lease "*)
            printf "%s\n" "{\"apiVersion\":\"coordination.k8s.io/v1\",\"kind\":\"Lease\",\"metadata\":{\"name\":\"mlflow-deploy\",\"namespace\":\"mlflow-system\",\"resourceVersion\":\"11\"},\"spec\":{\"holderIdentity\":\"new-holder\",\"renewTime\":\"2099-01-01T00:00:00Z\",\"leaseDurationSeconds\":21600}}"
            ;;
          release-own:*" get lease "*)
            printf "{\"apiVersion\":\"coordination.k8s.io/v1\",\"kind\":\"Lease\",\"metadata\":{\"name\":\"mlflow-deploy\",\"namespace\":\"mlflow-system\",\"resourceVersion\":\"9\"},\"spec\":{\"holderIdentity\":\"%s\",\"renewTime\":\"2099-01-01T00:00:00Z\",\"leaseDurationSeconds\":21600}}\n" "$LEASE_HOLDER"
            ;;
          create-race:*" create -f - -o json"*) return 44 ;;
          *" replace -f - -o json"*)
            local payload
            payload="$(</dev/stdin)"
            printf "%s" "$payload" >"${CASE_DIR}/replace.json"
            jq ".metadata.resourceVersion = \"updated\"" <<<"$payload"
            ;;
          *) return 0 ;;
        esac
      }

      source "$DEPLOY_PATH"
      initialize_deploy_identity

      case "$CASE_NAME" in
        active)
          if acquire_deploy_lease >/dev/null 2>&1; then
            exit 41
          fi
          [[ ! -e "${CASE_DIR}/replace.json" ]]
          ;;
        create-race)
          if acquire_deploy_lease >/dev/null 2>&1; then
            exit 43
          fi
          [[ ! -e "${CASE_DIR}/replace.json" ]]
          ;;
        stale)
          if acquire_deploy_lease >/dev/null 2>&1; then
            exit 44
          fi
          [[ ! -e "${CASE_DIR}/replace.json" ]]
          ;;
        release-other)
          LEASE_ACQUIRED=true
          if release_deploy_lease >/dev/null 2>&1; then
            exit 42
          fi
          [[ ! -e "${CASE_DIR}/replace.json" ]]
          ;;
        release-own)
          LEASE_ACQUIRED=true
          release_deploy_lease >/dev/null
          jq -e ".metadata.resourceVersion == \"9\" and .spec.holderIdentity == \"\"" "${CASE_DIR}/replace.json" >/dev/null
          ;;
      esac
    '
}

run_lease_case active
run_lease_case create-race
run_lease_case stale
run_lease_case release-other
run_lease_case release-own

trap_log="${fault_dir}/trap.log"
if CASE_DIR="$fault_dir" DEPLOY_PATH="$DEPLOY" MLFLOW_DEPLOY_RUN_ID='trap-run' \
  bash -c '
    set -euo pipefail
    source "$DEPLOY_PATH"
    initialize_deploy_identity
    release_deploy_lease() {
      printf "released %s\n" "$LEASE_HOLDER" >>"${CASE_DIR}/trap.log"
      LEASE_ACQUIRED=false
      return 0
    }
    LEASE_ACQUIRED=true
    install_deploy_traps
    exit 37
  '; then
  trap_status=0
else
  trap_status=$?
fi
[[ "$trap_status" == '37' ]] || {
  echo "deployment EXIT trap changed failure status to ${trap_status}" >&2
  exit 1
}
grep -Fq 'released mlflow-deploy-trap-run' "$trap_log" || {
  echo 'deployment EXIT trap did not release its Lease' >&2
  exit 1
}

signal_log="${fault_dir}/signal.log"
if CASE_DIR="$fault_dir" DEPLOY_PATH="$DEPLOY" MLFLOW_DEPLOY_RUN_ID='signal-run' \
  bash -c '
    set -euo pipefail
    source "$DEPLOY_PATH"
    initialize_deploy_identity
    release_deploy_lease() {
      printf "released %s\n" "$LEASE_HOLDER" >>"${CASE_DIR}/signal.log"
      LEASE_ACQUIRED=false
      return 0
    }
    LEASE_ACQUIRED=true
    install_deploy_traps
    kill -TERM "$$"
    exit 99
  '; then
  signal_status=0
else
  signal_status=$?
fi
[[ "$signal_status" == '143' ]] || {
  echo "deployment TERM trap returned ${signal_status} instead of 143" >&2
  exit 1
}
grep -Fq 'released mlflow-deploy-signal-run' "$signal_log" || {
  echo 'deployment TERM trap did not release its Lease' >&2
  exit 1
}

run_job_failure_case() {
  local scenario="$1"
  local case_dir="${fault_dir}/job-${scenario}"
  mkdir -p "$case_dir"

  CASE_NAME="$scenario" CASE_DIR="$case_dir" DEPLOY_PATH="$DEPLOY" MLFLOW_DEPLOY_RUN_ID='run-one' \
    bash -c '
      set -euo pipefail
      readonly EXPECTED_JOB="mlflow-artifact-acceptance-run-one"

      kubectl() {
        printf "%s\n" "$*" >>"${CASE_DIR}/calls.log"
        case "${CASE_NAME}: $*" in
          *:*" create --dry-run=client -f "*)
            printf "%s\n" "{\"apiVersion\":\"batch/v1\",\"kind\":\"Job\",\"metadata\":{\"name\":\"mlflow-artifact-acceptance\",\"namespace\":\"mlflow-system\"},\"spec\":{\"template\":{\"spec\":{\"restartPolicy\":\"Never\",\"containers\":[{\"name\":\"acceptance\",\"image\":\"example.invalid/mlflow\"}]}}}}"
            ;;
          create-absent:*" create -f - -o json"*) return 42 ;;
          create-absent:*" get job ${EXPECTED_JOB} --ignore-not-found -o json"*) return 0 ;;
          *:*" create -f - -o json"*)
            local payload
            payload="$(</dev/stdin)"
            jq ".metadata.uid = \"fresh-job-uid\"" <<<"$payload"
            ;;
          *:*" get job ${EXPECTED_JOB} -o jsonpath="*) printf "%s" "fresh-job-uid" ;;
          replacement:*" get job ${EXPECTED_JOB} -o json")
            printf "{\"metadata\":{\"name\":\"%s\",\"uid\":\"replacement-uid\"},\"status\":{\"conditions\":[{\"type\":\"Complete\",\"status\":\"True\"}]}}\n" "$EXPECTED_JOB"
            ;;
          *:*" get job ${EXPECTED_JOB} -o json")
            printf "{\"metadata\":{\"name\":\"%s\",\"uid\":\"fresh-job-uid\"},\"status\":{\"conditions\":[{\"type\":\"Complete\",\"status\":\"True\"}]}}\n" "$EXPECTED_JOB"
            ;;
          wait-failure:*" wait "*) return 43 ;;
          *:*" wait "*) return 0 ;;
          *:*"delete --raw "*)
            local delete_options
            delete_options="$(</dev/stdin)"
            printf "raw-delete %s %s\n" "$*" "$delete_options" >>"${CASE_DIR}/calls.log"
            [[ "$CASE_NAME" != replacement ]]
            ;;
          *) return 0 ;;
        esac
      }

      source "$DEPLOY_PATH"
      initialize_deploy_identity
      if run_job "$EXPECTED_JOB" /tmp/artifact-acceptance.yaml >/dev/null 2>&1; then
        exit 61
      fi
      case "$CASE_NAME" in
        create-absent)
          ! grep -Fq " wait " "${CASE_DIR}/calls.log"
          [[ "${LEASE_RELEASE_BLOCKED:-false}" != true ]]
          ;;
        wait-failure)
          grep -Fq "raw-delete" "${CASE_DIR}/calls.log"
          grep -Fq "fresh-job-uid" "${CASE_DIR}/calls.log"
          ;;
        replacement)
          grep -Fq " wait " "${CASE_DIR}/calls.log"
          grep -Fq "raw-delete" "${CASE_DIR}/calls.log"
          grep -Fq "fresh-job-uid" "${CASE_DIR}/calls.log"
          ;;
      esac
    '
}

run_job_failure_case create-absent
run_job_failure_case wait-failure
run_job_failure_case replacement

create_recovered_dir="${fault_dir}/create-recovered"
mkdir -p "$create_recovered_dir"
CASE_DIR="$create_recovered_dir" DEPLOY_PATH="$DEPLOY" MLFLOW_DEPLOY_RUN_ID='run-one' \
  bash -c '
    set -euo pipefail
    readonly EXPECTED_JOB="mlflow-db-upgrade-run-one"

    kubectl() {
      printf "%s\n" "$*" >>"${CASE_DIR}/calls.log"
      case "$*" in
        *" create --dry-run=client -f "*)
          printf "%s\n" "{\"apiVersion\":\"batch/v1\",\"kind\":\"Job\",\"metadata\":{\"name\":\"mlflow-db-upgrade\",\"namespace\":\"mlflow-system\"},\"spec\":{\"template\":{\"spec\":{\"restartPolicy\":\"Never\",\"containers\":[{\"name\":\"upgrade\",\"image\":\"example.invalid/mlflow\"}]}}}}"
          ;;
        *" create -f - -o json"*) return 42 ;;
        *" get job ${EXPECTED_JOB} --ignore-not-found -o json"*)
          printf "{\"metadata\":{\"name\":\"%s\",\"uid\":\"recovered-job-uid\",\"labels\":{\"platform.wellspiking.ai/deploy-run-id\":\"run-one\"}}}\n" "$EXPECTED_JOB"
          ;;
        *" get job ${EXPECTED_JOB} -o jsonpath="*) printf "%s" "recovered-job-uid" ;;
        *" get job ${EXPECTED_JOB} -o json")
          printf "{\"metadata\":{\"name\":\"%s\",\"uid\":\"recovered-job-uid\"},\"status\":{\"conditions\":[{\"type\":\"Complete\",\"status\":\"True\"}]}}\n" "$EXPECTED_JOB"
          ;;
        *" wait "*) return 0 ;;
        *) return 0 ;;
      esac
    }

    source "$DEPLOY_PATH"
    initialize_deploy_identity
    run_job "$EXPECTED_JOB" /tmp/db-upgrade.yaml >/dev/null
    grep -Fq " wait " "${CASE_DIR}/calls.log"
  '

run_create_fail_closed_case() {
  local scenario="$1"
  local case_dir="${fault_dir}/create-${scenario}"
  mkdir -p "$case_dir"

  if CASE_NAME="$scenario" CASE_DIR="$case_dir" DEPLOY_PATH="$DEPLOY" MLFLOW_DEPLOY_RUN_ID='run-one' \
    bash -c '
      set -euo pipefail
      readonly EXPECTED_JOB="mlflow-db-upgrade-run-one"

      kubectl() {
        printf "%s\n" "$*" >>"${CASE_DIR}/calls.log"
        case "${CASE_NAME}: $*" in
          *:*" create --dry-run=client -f "*)
            printf "%s\n" "{\"apiVersion\":\"batch/v1\",\"kind\":\"Job\",\"metadata\":{\"name\":\"mlflow-db-upgrade\",\"namespace\":\"mlflow-system\"},\"spec\":{\"template\":{\"spec\":{\"restartPolicy\":\"Never\",\"containers\":[{\"name\":\"upgrade\",\"image\":\"example.invalid/mlflow\"}]}}}}"
            ;;
          *:*" create -f - -o json"*) return 42 ;;
          uncertain:*" get job ${EXPECTED_JOB} --ignore-not-found -o json"*) return 45 ;;
          mismatch:*" get job ${EXPECTED_JOB} --ignore-not-found -o json"*)
            printf "{\"metadata\":{\"name\":\"%s\",\"uid\":\"foreign-job-uid\",\"labels\":{\"platform.wellspiking.ai/deploy-run-id\":\"other-run\"}}}\n" "$EXPECTED_JOB"
            ;;
          *) return 0 ;;
        esac
      }

      source "$DEPLOY_PATH"
      initialize_deploy_identity
      release_deploy_lease() {
        printf "released\n" >"${CASE_DIR}/released.log"
        LEASE_ACQUIRED=false
        return 0
      }
      LEASE_ACQUIRED=true
      install_deploy_traps
      if run_job "$EXPECTED_JOB" /tmp/db-upgrade.yaml >/dev/null 2>"${CASE_DIR}/stderr.log"; then
        exit 82
      fi
      [[ "${LEASE_RELEASE_BLOCKED:-false}" == true ]]
      grep -Fq "Lease retained" "${CASE_DIR}/stderr.log"
      exit 73
    '; then
    case_status=0
  else
    case_status=$?
  fi
  [[ "$case_status" == '73' ]] || {
    echo "${scenario} create ambiguity exited ${case_status} instead of preserving the original failure" >&2
    exit 1
  }
  if [[ -e "${case_dir}/released.log" ]]; then
    echo "${scenario} create ambiguity released the deployment Lease" >&2
    exit 1
  fi
}

run_create_fail_closed_case uncertain
run_create_fail_closed_case mismatch

job_case_dir="${fault_dir}/unique-job"
mkdir -p "$job_case_dir"
CASE_DIR="$job_case_dir" DEPLOY_PATH="$DEPLOY" MLFLOW_DEPLOY_RUN_ID='run-one' \
  bash -c '
    set -euo pipefail
    readonly EXPECTED_JOB="mlflow-db-upgrade-run-one"

    kubectl() {
      printf "%s\n" "$*" >>"${CASE_DIR}/calls.log"
      case "$*" in
        *" create --dry-run=client -f "*)
          printf "%s\n" "{\"apiVersion\":\"batch/v1\",\"kind\":\"Job\",\"metadata\":{\"name\":\"mlflow-db-upgrade\",\"namespace\":\"mlflow-system\",\"labels\":{}},\"spec\":{\"template\":{\"spec\":{\"restartPolicy\":\"Never\",\"containers\":[{\"name\":\"upgrade\",\"image\":\"example.invalid/mlflow\"}]}}}}"
          ;;
        *" create -f - -o json"*)
          local payload
          payload="$(</dev/stdin)"
          printf "%s" "$payload" >"${CASE_DIR}/created.json"
          jq ".metadata.uid = \"fresh-job-uid\"" <<<"$payload"
          ;;
        *" get job ${EXPECTED_JOB} -o jsonpath="*) printf "%s" "fresh-job-uid" ;;
        *" get job ${EXPECTED_JOB} -o json")
          printf "{\"metadata\":{\"name\":\"%s\",\"uid\":\"fresh-job-uid\"},\"status\":{\"conditions\":[{\"type\":\"Complete\",\"status\":\"True\"}]}}\n" "$EXPECTED_JOB"
          ;;
        *" wait "*) return 0 ;;
        *) return 0 ;;
      esac
    }

    source "$DEPLOY_PATH"
    initialize_deploy_identity
    [[ "$(deployment_job_name mlflow-db-upgrade)" == "$EXPECTED_JOB" ]]
    run_job "$EXPECTED_JOB" /tmp/db-upgrade.yaml >/dev/null
    jq -e ".metadata.name == \"${EXPECTED_JOB}\" and .metadata.labels[\"platform.wellspiking.ai/deploy-run-id\"] == \"run-one\"" "${CASE_DIR}/created.json" >/dev/null
    ! grep -Fq " delete job " "${CASE_DIR}/calls.log"
  '

grep -Fq 'run_job "$(deployment_job_name mlflow-artifact-storage-probe)"' "$DEPLOY"
grep -Fq 'run_job "$(deployment_job_name mlflow-db-upgrade)"' "$DEPLOY"
grep -Fq 'run_job "$(deployment_job_name mlflow-artifact-acceptance)"' "$DEPLOY"
grep -Fq 'Kubernetes Lease' "$README"
grep -Fq '任意非空 holder' "$README"
grep -Fq '手动解锁' "$README"
grep -Fq 'resourceVersion' "$README"

echo 'MLflow deployment concurrency contract verified'
