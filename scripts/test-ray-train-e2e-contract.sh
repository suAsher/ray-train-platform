#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly runtime_harness="${root_dir}/scripts/e2e-ray-runtime-upgrade.sh"
readonly managed_harness="${root_dir}/scripts/e2e-ray-train-managed.sh"
readonly fault_harness="${root_dir}/scripts/e2e-ray-train-faults.sh"
readonly shared_harness="${root_dir}/scripts/e2e-training.sh"

fail() {
  echo "Ray Train E2E contract failed: $*" >&2
  exit 1
}

require_file() {
  [[ -f "$1" ]] || fail "missing file: $1"
}

require_in() {
  grep -Fq -- "$2" "$1" || fail "$(basename -- "$1") missing: $2"
}

for script in "$runtime_harness" "$managed_harness" "$fault_harness" "$shared_harness"; do
  require_file "$script"
  [[ -x "$script" ]] || fail "script is not executable: $script"
  require_in "$script" 'set -euo pipefail'
  bash -n "$script"
done

for topology in 1gpu 8gpu 2x8gpu; do
  require_in "$runtime_harness" "$topology"
  require_in "$managed_harness" "$topology"
done
for flow in portal spk-rayjob native-ray; do
  require_in "$runtime_harness" "$flow"
  require_in "$managed_harness" "$flow"
done
for expected in \
  'ray-ddp' '2.56.1' 'verify_persisted_job' 'submissionOrigin' \
  'trainingEngine' 'rayVersion' 'workerReplicas' 'gpusPerWorker' \
  'PORTAL_SESSION_CONFIG' '/api/v1/jobs' 'verify_acceptance_identity'; do
  require_in "$runtime_harness" "$expected"
done
for expected in \
  'ray-train' 'expected_workers=16' 'training-performance' '/logs' \
  'mlflow-dashboard-access' 'dashboard-access' '/checkpoints' 'complete' \
  'resume-from-job' 'parentJobId' 'resumeCheckpointId' \
  'services' 'persistentvolumeclaims' 'rayclusters.ray.io' \
  'workloads.kueue.x-k8s.io' 'gpuallocations' 'successTtlSeconds' \
  'verify_acceptance_identity'; do
  require_in "$managed_harness" "$expected"
done
require_in "$managed_harness" 'resource_json='
require_in "$managed_harness" 'ledger_record gpuallocation'
require_in "$managed_harness" 'jobs.batch'
require_in "$managed_harness" 'pods'
for expected in \
  'ALLOW_DESTRUCTIVE_FAULT_TESTS' 'ACCEPTANCE_JOB_ID' 'ACCEPTANCE_LABEL_VALUE' \
  'worker-process-exit' 'worker-pod-delete' 'dns-transient' 'tos-transient' \
  'gpu-node-restart' 'driver-head-failure' 'CONFIRM_GPU_NODE_RESTART' \
  'unrelated GPU workload' '--namespace' 'ray.io/job-id'; do
  require_in "$fault_harness" "$expected"
done
require_in "$fault_harness" 'acceptance job name does not match ACCEPTANCE_PREFIX'
require_in "$fault_harness" 'kube delete dnschaos.chaos-mesh.org'
require_in "$fault_harness" 'kube delete networkchaos.chaos-mesh.org'
for expected in \
  'validate_acceptance_prefix' 'ledger_init' 'ledger_record' 'ledger_cleanup' \
  '.tmp' 'mv --' 'RUN_RAY_TRAIN_E2E' 'DRY_RUN' 'entrypoint_for_engine' \
  'python ddp_smoke.py' 'python ray_train_smoke.py' 'max-time'; do
  require_in "$shared_harness" "$expected"
done
require_in "$shared_harness" 'Bearer token contains unsafe characters'
require_in "$shared_harness" 'sourceartifact'
if grep -Fq -- '-- sh -ceu' "$shared_harness"; then
  fail 'native managed submission must pass the validated Python entrypoint directly'
fi

temporary="$(mktemp -d)"
trap 'rm -rf "$temporary"' EXIT
mkdir -p "$temporary/bin"
command_log="$temporary/commands.log"
: >"$command_log"
for command_name in curl kubectl docker spk-rayjob; do
  cat >"$temporary/bin/$command_name" <<'FIXTURE'
#!/usr/bin/env bash
printf '%s %s\n' "$(basename -- "$0")" "$*" >>"${E2E_COMMAND_LOG}"
exit 97
FIXTURE
  chmod +x "$temporary/bin/$command_name"
done

for harness in "$runtime_harness" "$managed_harness" "$fault_harness" "$shared_harness"; do
  output="$(PATH="$temporary/bin:$PATH" E2E_COMMAND_LOG="$command_log" "$harness")" || fail "default dry-run failed: $harness"
  grep -Fq 'DRY_RUN' <<<"$output" || fail "default mode is not visibly dry-run: $harness"
done
[[ ! -s "$command_log" ]] || fail 'default harness mode invoked an external command'

[[ "$("$runtime_harness" | grep -c '^CASE ')" -eq 9 ]] || fail 'runtime dry-run does not cover all nine matrix cases'
[[ "$("$managed_harness" | grep -c '^CASE ')" -eq 9 ]] || fail 'managed dry-run does not cover all nine matrix cases'

for unsafe_prefix in '' all acceptance 'acc-*' 'ACC-uppercase' 'acc-short'; do
  if PATH="$temporary/bin:$PATH" E2E_COMMAND_LOG="$command_log" ACCEPTANCE_PREFIX="$unsafe_prefix" "$runtime_harness" >/dev/null 2>&1; then
    fail "runtime harness accepted unsafe prefix: ${unsafe_prefix:-<empty>}"
  fi
done

first_prefix="$("$runtime_harness" | sed -n 's/^ACCEPTANCE_PREFIX=//p' | head -1)"
second_prefix="$("$runtime_harness" | sed -n 's/^ACCEPTANCE_PREFIX=//p' | head -1)"
[[ "$first_prefix" =~ ^acc-[a-z0-9][a-z0-9-]{7,35}[a-z0-9]$ ]] || fail "generated prefix is unsafe: $first_prefix"
[[ "$second_prefix" =~ ^acc-[a-z0-9][a-z0-9-]{7,35}[a-z0-9]$ ]] || fail "generated prefix is unsafe: $second_prefix"
[[ "$first_prefix" != "$second_prefix" ]] || fail 'acceptance prefixes are not unique'

ledger="$temporary/cleanup-ledger.tsv"
# shellcheck source=scripts/e2e-training.sh
source "$shared_harness"
ACCEPTANCE_PREFIX='acc-contract-1234'
CLEANUP_LEDGER="$ledger"
ledger_init
ledger_record job job-0123456789abcdef01234567 tenant-contract
ledger_record raycluster acc-contract-cluster tenant-contract
[[ -s "$ledger" && ! -e "$ledger.tmp" ]] || fail 'cleanup ledger was not atomically published'
grep -Fq $'job\tjob-0123456789abcdef01234567\ttenant-contract\tacc-contract-1234' "$ledger" || fail 'job ID was not recorded in the ledger'
grep -Fq $'raycluster\tacc-contract-cluster\ttenant-contract\tacc-contract-1234' "$ledger" || fail 'resource ID was not recorded in the ledger'
ledger_before="$(sha256sum "$ledger" 2>/dev/null || shasum -a 256 "$ledger")"
if ledger_init >/dev/null 2>&1; then
  fail 'ledger initialization overwrote an existing acceptance ledger'
fi
ledger_after="$(sha256sum "$ledger" 2>/dev/null || shasum -a 256 "$ledger")"
[[ "$ledger_before" == "$ledger_after" ]] || fail 'failed ledger reinitialization changed cleanup evidence'

# A failed scoped discovery must never be mistaken for an empty/clean result.
# shellcheck source=scripts/e2e-ray-train-managed.sh
source "$managed_harness"
kube() { return 42; }
if discover_owned_resources job-0123456789abcdef01234567 tenant-contract >/dev/null 2>&1; then
  fail 'managed resource discovery treated a failed Kubernetes query as empty'
fi

scoped_log="$temporary/scoped-cleanup.log"
: >"$scoped_log"
kube() {
  printf 'kube %s\n' "$*" >>"$scoped_log"
  printf '{"items":[]}\n'
}
portal_api_request() {
  printf 'portal %s %s\n' "$1" "$2" >>"$scoped_log"
  printf '{"data":[]}\n'
}
wait_for_ttl_cleanup job-0123456789abcdef01234567 tenant-contract >/dev/null
for resource in services persistentvolumeclaims rayclusters.ray.io workloads.kueue.x-k8s.io; do
  grep -Fq "kube get $resource --namespace tenant-contract --selector ray.io/job-id=job-0123456789abcdef01234567 -o json" "$scoped_log" || fail "TTL check lost exact scope for $resource"
done
grep -Fq 'portal GET /api/v1/gpu-allocations' "$scoped_log" || fail 'TTL check omitted persisted GPU allocations'

fault_selector_log="$temporary/fault-selector.log"
: >"$fault_selector_log"
ALLOW_DESTRUCTIVE_FAULT_TESTS=1 \
ACCEPTANCE_PREFIX=acc-contract-1234 \
ACCEPTANCE_JOB_ID=job-0123456789abcdef01234567 \
ACCEPTANCE_LABEL_VALUE=job-0123456789abcdef01234567 \
FAULT_CASE=worker-pod-delete \
TENANT_NAMESPACE=tenant-contract \
FAULT_SELECTOR_LOG="$fault_selector_log" \
bash -c '
  source "$1"
  kube() {
    printf "%s\n" "$*" >>"$FAULT_SELECTOR_LOG"
    printf "{\"items\":[{\"metadata\":{\"name\":\"worker-pod-a\"}}]}\n"
  }
  [[ "$(select_one_pod "$worker_selector")" == worker-pod-a ]]
' fixture "$fault_harness"
grep -Fq 'get pods --namespace tenant-contract --selector ray.io/job-id=job-0123456789abcdef01234567,ray.io/node-type=worker -o json' "$fault_selector_log" || fail 'fault selection is not tenant/job/worker exact'
if grep -Fq -- '--all-namespaces' "$fault_selector_log"; then
  fail 'ordinary worker fault selection escaped the tenant namespace'
fi

: >"$command_log"
if PATH="$temporary/bin:$PATH" E2E_COMMAND_LOG="$command_log" ALLOW_DESTRUCTIVE_FAULT_TESTS=1 "$fault_harness" >/dev/null 2>&1; then
  fail 'fault harness accepted missing dedicated job identity'
fi
if PATH="$temporary/bin:$PATH" E2E_COMMAND_LOG="$command_log" \
  ALLOW_DESTRUCTIVE_FAULT_TESTS=1 ACCEPTANCE_JOB_ID=job-0123456789abcdef01234567 \
  ACCEPTANCE_LABEL_VALUE=job-0123456789abcdef01234567 FAULT_CASE=worker-pod-delete \
  "$fault_harness" >/dev/null 2>&1; then
  fail 'fault harness accepted destructive mode without an explicit acceptance prefix'
fi
if PATH="$temporary/bin:$PATH" E2E_COMMAND_LOG="$command_log" \
  ALLOW_DESTRUCTIVE_FAULT_TESTS=1 ACCEPTANCE_PREFIX=acc-contract-1234 ACCEPTANCE_JOB_ID=job-0123456789abcdef01234567 \
  ACCEPTANCE_LABEL_VALUE=wrong "$fault_harness" >/dev/null 2>&1; then
  fail 'fault harness accepted a mismatched acceptance label'
fi
if PATH="$temporary/bin:$PATH" E2E_COMMAND_LOG="$command_log" \
  ALLOW_DESTRUCTIVE_FAULT_TESTS=1 ACCEPTANCE_PREFIX=acc-contract-1234 ACCEPTANCE_JOB_ID=job-0123456789abcdef01234567 \
  ACCEPTANCE_LABEL_VALUE=job-0123456789abcdef01234567 FAULT_CASE=gpu-node-restart \
  TARGET_GPU_NODE=gpu-node-a CONFIRM_GPU_NODE_RESTART=gpu-node-b "$fault_harness" >/dev/null 2>&1; then
  fail 'fault harness accepted the wrong node confirmation'
fi
[[ ! -s "$command_log" ]] || fail 'fault authorization rejection invoked an external command'

echo 'Ray Train E2E contracts verified without cluster access'
