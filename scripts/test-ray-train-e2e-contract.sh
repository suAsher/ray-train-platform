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
require_in "$runtime_harness" "spk-rayjob) printf 'ray-cli"
for expected in \
  'ray-train' 'expected_workers=16' 'training-performance' '/logs' \
  'mlflow-dashboard-access' 'dashboard-access' '/checkpoints' 'complete' \
  'resume-from-job' 'parentJobId' 'resumeCheckpointId' \
  'verify_resume_consumption' 'ray-train-checkpoint-smoke-result.json' \
  'services' 'persistentvolumeclaims' 'rayclusters.ray.io' \
  'workloads.kueue.x-k8s.io' 'gpuallocations' 'successTtlSeconds' \
  'verify_acceptance_identity'; do
  require_in "$managed_harness" "$expected"
done
require_in "$managed_harness" '_validated_list_json'
require_in "$managed_harness" '_validated_exact_json'
require_in "$managed_harness" 'ledger_record gpuallocation'
require_in "$managed_harness" 'jobs.batch'
require_in "$managed_harness" 'pods'
for expected in \
  'ALLOW_DESTRUCTIVE_FAULT_TESTS' 'ACCEPTANCE_JOB_ID' 'ACCEPTANCE_LABEL_VALUE' \
  'worker-process-exit' 'worker-pod-delete' 'dns-transient' 'tos-transient' \
  'gpu-node-restart' 'driver-head-failure' 'CONFIRM_GPU_NODE_RESTART' \
  'unrelated GPU workload' '--namespace' 'platform_job_id'; do
  require_in "$fault_harness" "$expected"
done
require_in "$fault_harness" 'acceptance job name does not match ACCEPTANCE_PREFIX'
require_in "$fault_harness" 'kube delete dnschaos.chaos-mesh.org'
require_in "$fault_harness" 'kube delete networkchaos.chaos-mesh.org'
for expected in \
  'validate_acceptance_prefix' 'ledger_init' 'ledger_record' 'ledger_cleanup' \
  'submission-intent' 'reconcile_submission_intent' 'verify_interactive_portal_session' \
  "ACCEPTANCE_LABEL_KEY='platform_job_id'" "ACCEPTANCE_TENANT_LABEL_KEY='platform_tenant_id'" \
  '.tmp' 'mv --' 'RUN_RAY_TRAIN_E2E' 'DRY_RUN' 'entrypoint_for_engine' \
  'python ddp_smoke.py' 'python ray_train_smoke.py' 'max-time'; do
  require_in "$shared_harness" "$expected"
done
require_in "$shared_harness" 'Bearer token contains unsafe characters'
require_in "$shared_harness" 'sourceartifact'
require_in "$shared_harness" 'command -v curl'
require_in "$fault_harness" 'command -v spk-rayjob'
require_in "$fault_harness" '_secure_config_token "$SPK_RAYJOB_CONFIG"'
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
ledger_record submission-intent acc-contract-portal tenant-contract acc-contract-portal
ledger_record job job-0123456789abcdef01234567 tenant-contract
ledger_record raycluster acc-contract-cluster tenant-contract
[[ -s "$ledger" && ! -e "$ledger.tmp" ]] || fail 'cleanup ledger was not atomically published'
grep -Fq $'job\tjob-0123456789abcdef01234567\ttenant-contract\tacc-contract-1234' "$ledger" || fail 'job ID was not recorded in the ledger'
grep -Fq $'submission-intent\tacc-contract-portal\ttenant-contract\tacc-contract-1234\tacc-contract-portal' "$ledger" || fail 'submission intent was not recorded before side effects'
grep -Fq $'raycluster\tacc-contract-cluster\ttenant-contract\tacc-contract-1234' "$ledger" || fail 'resource ID was not recorded in the ledger'
ledger_before="$(sha256sum "$ledger" 2>/dev/null || shasum -a 256 "$ledger")"
if ledger_init >/dev/null 2>&1; then
  fail 'ledger initialization overwrote an existing acceptance ledger'
fi
ledger_after="$(sha256sum "$ledger" 2>/dev/null || shasum -a 256 "$ledger")"
[[ "$ledger_before" == "$ledger_after" ]] || fail 'failed ledger reinitialization changed cleanup evidence'

ddp_payload="$(TRAINING_IMAGE='registry.example/train@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' PORTAL_SOURCE_SNAPSHOT_ID=snapshot-contract build_portal_payload acc-contract-ddp ray-ddp 1 1 single_gpu '')"
managed_payload="$(TRAINING_IMAGE='registry.example/train@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' PORTAL_SOURCE_SNAPSHOT_ID=snapshot-contract build_portal_payload acc-contract-managed ray-train 1 1 single_gpu '')"
jq -e '.spec.trainingEngine == "ray-ddp" and (.spec | has("managed") | not)' <<<"$ddp_payload" >/dev/null || fail 'Portal ray-ddp payload violates the domain managed-policy contract'
jq -e '.spec.trainingEngine == "ray-train" and .spec.managed.maxFailures == 2' <<<"$managed_payload" >/dev/null || fail 'Portal ray-train payload lost its managed policy'

portal_api_request() { printf '{"data":{"authType":"pat"}}\n'; }
if verify_interactive_portal_session >/dev/null 2>&1; then
  fail 'Portal session accepted a PAT'
fi
portal_api_request() { printf '{"data":{"authType":"oidc"}}\n'; }
verify_interactive_portal_session >/dev/null || fail 'Portal session rejected OIDC'
portal_api_request() { printf '{"data":{"authType":"local"}}\n'; }
verify_interactive_portal_session >/dev/null || fail 'Portal session rejected local auth'

response_loss_ledger="$temporary/response-loss-ledger.tsv"
CLEANUP_LEDGER="$response_loss_ledger"
ledger_init
submit_portal_job() {
  grep -Fq $'submission-intent\tacc-contract-1234-lost\ttenant-contract' "$CLEANUP_LEDGER" || fail 'submission side effect ran before atomic intent publication'
  return 28
}
reconcile_submission_intent() {
  [[ "$1" == portal && "$2" == acc-contract-1234-lost ]] || return 1
  printf 'job-aaaaaaaaaaaaaaaaaaaaaaaa\n'
}
reconciled="$(TENANT_NAMESPACE=tenant-contract submit_acceptance_job portal acc-contract-1234-lost ray-ddp 1 1 single_gpu '')"
[[ "$reconciled" == job-aaaaaaaaaaaaaaaaaaaaaaaa ]] || fail 'response loss was not reconciled by exact acceptance identity'
for lost_flow in spk-rayjob native-ray; do
  CLEANUP_LEDGER="$temporary/${lost_flow}-response-loss-ledger.tsv"
  ledger_init
  submit_spk_job() { return 28; }
  submit_native_ray_job() { return 28; }
  reconcile_submission_intent() {
    [[ "$1" == "$lost_flow" ]] || return 1
    if [[ "$lost_flow" == native-ray ]]; then
      [[ "$(submission_identity_for_flow "$1" "$2")" == acc_contract_1234_native_lost ]] || return 1
    else
      [[ "$2" == acc-contract-1234-spk-lost ]] || return 1
    fi
    printf 'job-bbbbbbbbbbbbbbbbbbbbbbbb\n'
  }
  if [[ "$lost_flow" == native-ray ]]; then lost_name=acc-contract-1234-native-lost; else lost_name=acc-contract-1234-spk-lost; fi
  reconciled="$(TENANT_NAMESPACE=tenant-contract submit_acceptance_job "$lost_flow" "$lost_name" ray-ddp 1 1 single_gpu '')"
  [[ "$reconciled" == job-bbbbbbbbbbbbbbbbbbbbbbbb ]] || fail "$lost_flow response loss was not reconciled by exact acceptance identity"
done

side_effect_marker="$temporary/invalid-name-side-effect"
submit_spk_job() { : >"$side_effect_marker"; printf 'job-cccccccccccccccccccccccc\n'; }
if TENANT_NAMESPACE=tenant-contract submit_acceptance_job spk-rayjob foreign-job ray-ddp 1 1 single_gpu '' >/dev/null 2>&1; then
  fail 'submission accepted a name outside the real acceptance prefix'
fi
[[ ! -e "$side_effect_marker" ]] || fail 'invalid acceptance identity reached a submission side effect'

# A failed scoped discovery must never be mistaken for an empty/clean result.
# shellcheck source=scripts/e2e-ray-train-managed.sh
source "$managed_harness"
kube() { return 42; }
resource_detail='{"data":{"id":"job-0123456789abcdef01234567","tenantId":"tenant-a","kubernetesNamespace":"tenant-contract","rayJobName":"rayjob-a","rayJobUid":"rayjob-uid-a","rayClusterName":"cluster-a"}}'
if discover_owned_resources "$resource_detail" >/dev/null 2>&1; then
  fail 'managed resource discovery treated a failed Kubernetes query as empty'
fi

kube() { printf '{malformed\n'; }
if discover_owned_resources "$resource_detail" >/dev/null 2>&1; then
  fail 'managed resource discovery treated malformed JSON as an empty result'
fi

owned_ledger="$temporary/owned-ledger.tsv"
CLEANUP_LEDGER="$owned_ledger"
ledger_init
kube() {
  case "$*" in
    'get rayclusters.ray.io cluster-a '*) printf '{"metadata":{"name":"cluster-a","uid":"cluster-uid-a","ownerReferences":[{"kind":"RayJob","name":"rayjob-a","uid":"rayjob-uid-a"}]}}\n' ;;
    'get pods '*) printf '{"items":[{"metadata":{"name":"worker-pod-a","uid":"pod-uid-a","labels":{"platform_job_id":"job-0123456789abcdef01234567","platform_tenant_id":"tenant-a"}},"spec":{"volumes":[{"name":"local-cache-data1","ephemeral":{"volumeClaimTemplate":{}}}]}}]}\n' ;;
    'get persistentvolumeclaims worker-pod-a-local-cache-data1 '*) printf '{"metadata":{"name":"worker-pod-a-local-cache-data1","ownerReferences":[{"kind":"Pod","name":"worker-pod-a","uid":"pod-uid-a"}]}}\n' ;;
    'get services '*) printf '{"items":[{"metadata":{"name":"cluster-a-head-svc","ownerReferences":[{"kind":"RayCluster","name":"cluster-a","uid":"cluster-uid-a"}]}}]}\n' ;;
    'get workloads.kueue.x-k8s.io '*) printf '{"items":[{"metadata":{"name":"rayjob-a-workload","ownerReferences":[{"kind":"RayJob","name":"rayjob-a","uid":"rayjob-uid-a"}]}}]}\n' ;;
    'get jobs.batch '*) printf '{"items":[{"metadata":{"name":"rayjob-a-submitter","ownerReferences":[{"kind":"RayJob","name":"rayjob-a","uid":"rayjob-uid-a"}]}}]}\n' ;;
    *) return 43 ;;
  esac
}
discover_owned_resources "$resource_detail" || fail 'exact owner-reference resource fixture was rejected'
for row in \
  $'raycluster\tcluster-a' $'pod\tworker-pod-a' \
  $'pvc\tworker-pod-a-local-cache-data1' $'service\tcluster-a-head-svc' \
  $'workload\trayjob-a-workload' $'k8sjob\trayjob-a-submitter'; do
  grep -Fq "$row" "$owned_ledger" || fail "owned resource was not recorded: $row"
done

CLEANUP_LEDGER="$temporary/foreign-owner-ledger.tsv"
ledger_init
kube() {
  case "$*" in
    'get rayclusters.ray.io cluster-a '*) printf '{"metadata":{"name":"cluster-a","uid":"cluster-uid-a","ownerReferences":[{"kind":"RayJob","name":"rayjob-a","uid":"rayjob-uid-a"}]}}\n' ;;
    'get pods '*) printf '{"items":[]}\n' ;;
    'get services '*) printf '{"items":[{"metadata":{"name":"foreign-exporter","ownerReferences":[{"kind":"RayCluster","name":"other-cluster","uid":"other-uid"}]}}]}\n' ;;
    *) printf '{"items":[]}\n' ;;
  esac
}
if discover_owned_resources "$resource_detail" >/dev/null 2>&1; then
  fail 'resource discovery accepted a Service from a foreign owner'
fi
[[ "$(wc -l <"$CLEANUP_LEDGER" | tr -d ' ')" -eq 1 ]] || fail 'failed owner validation partially polluted the cleanup ledger'

scoped_log="$temporary/scoped-cleanup.log"
: >"$scoped_log"
kube() {
  printf 'kube %s\n' "$*" >>"$scoped_log"
  case "$*" in
    'get rayclusters.ray.io cluster-a '*) printf '\n' ;;
    *) printf '{"items":[]}\n' ;;
  esac
}
portal_api_request() {
  printf 'portal %s %s\n' "$1" "$2" >>"$scoped_log"
  printf '{"data":[]}\n'
}
wait_for_ttl_cleanup "$resource_detail" >/dev/null
grep -Fq 'kube get pods --namespace tenant-contract --selector platform_job_id=job-0123456789abcdef01234567,platform_tenant_id=tenant-a -o json' "$scoped_log" || fail 'pod discovery lost exact persisted job/tenant labels'
grep -Fq 'kube get services --namespace tenant-contract --selector ray.io/cluster=cluster-a -o json' "$scoped_log" || fail 'Service discovery lost exact persisted RayCluster identity'
grep -Fq 'kube get rayclusters.ray.io cluster-a --namespace tenant-contract --ignore-not-found -o json' "$scoped_log" || fail 'RayCluster discovery is not an exact-name lookup'
grep -Fq 'kube get workloads.kueue.x-k8s.io --namespace tenant-contract --selector kueue.x-k8s.io/job-uid=rayjob-uid-a -o json' "$scoped_log" || fail 'Workload discovery lost exact persisted RayJob UID'
grep -Fq 'kube get jobs.batch --namespace tenant-contract --selector ray.io/originated-from-cr-name=rayjob-a -o json' "$scoped_log" || fail 'submitter Job discovery lost exact persisted RayJob identity'
grep -Fq 'portal GET /api/v1/gpu-allocations' "$scoped_log" || fail 'TTL check omitted persisted GPU allocations'

resume_calls="$temporary/resume-calls.log"
: >"$resume_calls"
portal_api_request() {
  printf '%s\n' "$2" >>"$resume_calls"
  case "$2" in
    */logs*) printf '{"data":{"items":[{"line":"RAY_TRAIN_CHECKPOINT_SMOKE rank=0 world_size=16 step=8 resumed=True"}]}}\n' ;;
    */artifacts/preview*) printf '{"data":{"content":"{\\"resumed\\":true,\\"parentStep\\":7,\\"firstStep\\":8}"}}\n' ;;
    *) return 1 ;;
  esac
}
verify_resume_consumption job-0123456789abcdef01234567 7 >/dev/null || fail 'resume acceptance did not prove checkpoint consumption'
grep -Fq 'ray-train-checkpoint-smoke-result.json' "$resume_calls" || fail 'resume acceptance did not inspect the persisted result artifact'

fault_selector_log="$temporary/fault-selector.log"
: >"$fault_selector_log"
ALLOW_DESTRUCTIVE_FAULT_TESTS=1 \
ACCEPTANCE_PREFIX=acc-contract-1234 \
ACCEPTANCE_JOB_ID=job-0123456789abcdef01234567 \
ACCEPTANCE_LABEL_VALUE=job-0123456789abcdef01234567 \
ACCEPTANCE_TENANT_ID=tenant-a \
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
grep -Fq 'get pods --namespace tenant-contract --selector platform_job_id=job-0123456789abcdef01234567,platform_tenant_id=tenant-a,ray.io/node-type=worker -o json' "$fault_selector_log" || fail 'fault selection is not tenant/job/worker exact'
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

insecure_fault_config="$temporary/insecure-fault-config.json"
printf '{"token":"abcdefghijklmnop"}\n' >"$insecure_fault_config"
chmod 644 "$insecure_fault_config"
if PATH="$temporary/bin:$PATH" RUN_RAY_TRAIN_E2E=1 DRY_RUN=0 API_URL=https://platform.example \
  TENANT_NAMESPACE=tenant-contract SPK_RAYJOB_CONFIG="$insecure_fault_config" \
  bash -c 'source "$1"; require_fault_live_configuration' fixture "$fault_harness" >/dev/null 2>&1; then
  fail 'fault harness accepted a session config that was not mode 0600'
fi

echo 'Ray Train E2E contracts verified without cluster access'
