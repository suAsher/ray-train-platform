#!/usr/bin/env bash
set -euo pipefail

E2E_ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
DRY_RUN="${DRY_RUN:-1}"
RUN_RAY_TRAIN_E2E="${RUN_RAY_TRAIN_E2E:-0}"
E2E_TIMEOUT_SECONDS="${E2E_TIMEOUT_SECONDS:-1800}"
E2E_POLL_SECONDS="${E2E_POLL_SECONDS:-5}"
E2E_COMMAND_TIMEOUT_SECONDS="${E2E_COMMAND_TIMEOUT_SECONDS:-60}"
ACCEPTANCE_LABEL_KEY='platform_job_id'
ACCEPTANCE_TENANT_LABEL_KEY='platform_tenant_id'
CLEANUP_LEDGER="${CLEANUP_LEDGER:-}"

e2e_die() {
  echo "Ray Train E2E failed: $*" >&2
  return 1
}

generate_acceptance_prefix() {
  printf 'acc-%s-%s-%s\n' "$(date +%s)" "$$" "$RANDOM"
}

validate_acceptance_prefix() {
  local value="${1:-}"
  [[ "$value" =~ ^acc-[a-z0-9][a-z0-9-]{7,23}[a-z0-9]$ ]] || { e2e_die 'ACCEPTANCE_PREFIX must be a unique 12-30 character lowercase acceptance name'; return 1; }
}

validate_tenant_namespace() {
  [[ "${1:-}" =~ ^tenant-[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || { e2e_die 'TENANT_NAMESPACE must be one concrete tenant namespace'; return 1; }
}

acceptance_setup() {
  if [[ "${ACCEPTANCE_PREFIX+x}" == x ]]; then
    validate_acceptance_prefix "$ACCEPTANCE_PREFIX" || return
  else
    ACCEPTANCE_PREFIX="$(generate_acceptance_prefix)" || return
    validate_acceptance_prefix "$ACCEPTANCE_PREFIX" || return
  fi
  if [[ -z "$CLEANUP_LEDGER" ]]; then
    CLEANUP_LEDGER="${TMPDIR:-/tmp}/ray-train-${ACCEPTANCE_PREFIX}-ledger.tsv"
  fi
  [[ "$CLEANUP_LEDGER" == /* && "$CLEANUP_LEDGER" != *$'\n'* && "$CLEANUP_LEDGER" != *'/../'* && "$CLEANUP_LEDGER" != */.. ]] || { e2e_die 'CLEANUP_LEDGER must be an absolute safe path'; return 1; }
  [[ "$E2E_TIMEOUT_SECONDS" =~ ^[1-9][0-9]*$ && "$E2E_TIMEOUT_SECONDS" -le 7200 ]] || { e2e_die 'E2E_TIMEOUT_SECONDS must be between 1 and 7200'; return 1; }
  [[ "$E2E_POLL_SECONDS" =~ ^[1-9][0-9]*$ && "$E2E_POLL_SECONDS" -le 60 ]] || { e2e_die 'E2E_POLL_SECONDS must be between 1 and 60'; return 1; }
  [[ "$E2E_COMMAND_TIMEOUT_SECONDS" =~ ^[1-9][0-9]*$ && "$E2E_COMMAND_TIMEOUT_SECONDS" -le 300 ]] || { e2e_die 'E2E_COMMAND_TIMEOUT_SECONDS must be between 1 and 300'; return 1; }
}

_ledger_parent() {
  local parent
  parent="$(dirname -- "$CLEANUP_LEDGER")" || return
  [[ -d "$parent" && ! -L "$parent" ]] || { e2e_die 'cleanup ledger parent must be an existing real directory'; return 1; }
  (cd -- "$parent" && pwd -P)
}

ledger_init() {
  validate_acceptance_prefix "$ACCEPTANCE_PREFIX" || return
  local parent temporary
  parent="$(_ledger_parent)" || return
  [[ ! -e "$CLEANUP_LEDGER" && ! -L "$CLEANUP_LEDGER" ]] || { e2e_die 'cleanup ledger already exists; choose a unique acceptance prefix or resume it explicitly'; return 1; }
  temporary="$(mktemp "${parent}/.$(basename -- "$CLEANUP_LEDGER").tmp.XXXXXXXX")" || return
  chmod 600 "$temporary" || return
  printf '# kind\tid\tnamespace\tacceptance-prefix\townership-label\n' >"$temporary" || return
  if ! ln -- "$temporary" "$CLEANUP_LEDGER"; then
    rm -f -- "$temporary"
    e2e_die 'cleanup ledger was created concurrently; refusing to overwrite it'
    return 1
  fi
  rm -f -- "$temporary" || return
}

ledger_resume() {
  validate_acceptance_prefix "$ACCEPTANCE_PREFIX" || return
  [[ -f "$CLEANUP_LEDGER" && ! -L "$CLEANUP_LEDGER" ]] || { e2e_die 'cleanup ledger is missing or unsafe'; return 1; }
  local permissions kind identifier namespace prefix ownership_label
  permissions="$(stat -f '%Lp' "$CLEANUP_LEDGER" 2>/dev/null || stat -c '%a' "$CLEANUP_LEDGER")" || return
  [[ "$permissions" == 600 ]] || { e2e_die 'cleanup ledger must be mode 0600'; return 1; }
  IFS= read -r kind <"$CLEANUP_LEDGER" || return
  [[ "$kind" == $'# kind\tid\tnamespace\tacceptance-prefix\townership-label' ]] || { e2e_die 'cleanup ledger header is invalid'; return 1; }
  while IFS=$'\t' read -r kind identifier namespace prefix ownership_label; do
    [[ -n "$kind" && "$kind" != \#* ]] || continue
    [[ "$prefix" == "$ACCEPTANCE_PREFIX" ]] || { e2e_die 'cleanup ledger contains a foreign acceptance prefix'; return 1; }
    [[ "$identifier" =~ ^[A-Za-z0-9][A-Za-z0-9._:-]{0,252}$ ]] || { e2e_die 'cleanup ledger contains an unsafe identifier'; return 1; }
    [[ "$namespace" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || { e2e_die 'cleanup ledger contains an unsafe namespace'; return 1; }
    [[ "$ownership_label" =~ ^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$ ]] || { e2e_die 'cleanup ledger contains an unsafe ownership label'; return 1; }
  done <"$CLEANUP_LEDGER"
}

ledger_record() {
  local kind="${1:-}" identifier="${2:-}" namespace="${3:-}" ownership_label="${4:-$ACCEPTANCE_PREFIX}"
  case "$kind" in
    submission-intent|job|sourceartifact-request|sourceartifact|rayjob|raycluster|k8sjob|service|pvc|workload|pod|gpuallocation|dnschaos|networkchaos|node-fault) ;;
    *) e2e_die "unsupported cleanup ledger kind: $kind"; return 1 ;;
  esac
  [[ "$identifier" =~ ^[A-Za-z0-9][A-Za-z0-9._:-]{0,252}$ ]] || { e2e_die 'cleanup ledger identifier is unsafe'; return 1; }
  [[ "$namespace" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || { e2e_die 'cleanup ledger namespace is unsafe'; return 1; }
  [[ "$ownership_label" =~ ^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$ ]] || { e2e_die 'cleanup ledger ownership label is unsafe'; return 1; }
  [[ -f "$CLEANUP_LEDGER" && ! -L "$CLEANUP_LEDGER" ]] || { e2e_die 'cleanup ledger is missing or unsafe'; return 1; }
  local parent temporary
  parent="$(_ledger_parent)" || return
  temporary="$(mktemp "${parent}/.$(basename -- "$CLEANUP_LEDGER").tmp.XXXXXXXX")" || return
  chmod 600 "$temporary" || return
  cp -- "$CLEANUP_LEDGER" "$temporary" || return
  printf '%s\t%s\t%s\t%s\t%s\n' "$kind" "$identifier" "$namespace" "$ACCEPTANCE_PREFIX" "$ownership_label" >>"$temporary" || return
  mv -- "$temporary" "$CLEANUP_LEDGER" || return
}

ledger_cleanup() {
  [[ -f "$CLEANUP_LEDGER" && ! -L "$CLEANUP_LEDGER" ]] || { e2e_die 'cleanup ledger is missing or unsafe'; return 1; }
  local kind identifier namespace prefix ownership_label actual_label resource
  while IFS=$'\t' read -r kind identifier namespace prefix ownership_label; do
    [[ -n "$kind" && "$kind" != \#* ]] || continue
    [[ "$prefix" == "$ACCEPTANCE_PREFIX" ]] || { echo "SKIP foreign ledger row: $kind $identifier" >&2; continue; }
    if [[ "${EXECUTE_LEDGER_CLEANUP:-0}" != 1 ]]; then
      printf 'CLEANUP_DRY_RUN kind=%s id=%s namespace=%s label=%s\n' "$kind" "$identifier" "$namespace" "$ownership_label"
      continue
    fi
    case "$kind" in
      rayjob) resource='rayjobs.ray.io' ;;
      raycluster) resource='rayclusters.ray.io' ;;
      service) resource='services' ;;
      pvc) resource='persistentvolumeclaims' ;;
      workload) resource='workloads.kueue.x-k8s.io' ;;
      pod) resource='pods' ;;
      k8sjob) resource='jobs.batch' ;;
      dnschaos) resource='dnschaos.chaos-mesh.org' ;;
      networkchaos) resource='networkchaos.chaos-mesh.org' ;;
      submission-intent|job|sourceartifact-request|sourceartifact|gpuallocation|node-fault)
        printf 'CLEANUP_MANUAL kind=%s id=%s namespace=%s\n' "$kind" "$identifier" "$namespace"
        continue
        ;;
      *) e2e_die "unsafe ledger kind during cleanup: $kind"; return 1 ;;
    esac
    actual_label="$(kube get "$resource" "$identifier" --namespace "$namespace" -o "jsonpath={.metadata.labels.${ACCEPTANCE_LABEL_KEY//./\\.}}")" || return
    [[ "$actual_label" == "$ownership_label" ]] || { echo "SKIP ownership mismatch: $resource/$identifier" >&2; continue; }
    kube delete "$resource" "$identifier" --namespace "$namespace" --wait=false
  done <"$CLEANUP_LEDGER"
}

e2e_live_enabled() {
  [[ "$RUN_RAY_TRAIN_E2E" == 1 && "$DRY_RUN" == 0 ]]
}

bounded_command() {
  command perl -e 'alarm shift; exec @ARGV or die "exec failed: $!\\n"' "$E2E_COMMAND_TIMEOUT_SECONDS" "$@"
}

kube() {
  bounded_command kubectl --request-timeout="${E2E_COMMAND_TIMEOUT_SECONDS}s" "$@"
}

entrypoint_for_engine() {
  case "$1" in
    ray-ddp) printf 'python ddp_smoke.py\n' ;;
    ray-train) printf 'python ray_train_smoke.py\n' ;;
    *) e2e_die "unsupported training engine: $1" ;;
  esac
}

require_live_configuration() {
  e2e_live_enabled || { e2e_die 'live execution requires RUN_RAY_TRAIN_E2E=1 and DRY_RUN=0'; return 1; }
  [[ "${API_URL:-}" =~ ^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?/?$ ]] || { e2e_die 'API_URL must be a concrete HTTPS origin'; return 1; }
  [[ "${TRAINING_IMAGE:-}" =~ ^[^@[:space:]]+@sha256:[0-9a-f]{64}$ ]] || { e2e_die 'TRAINING_IMAGE must be an immutable lowercase SHA-256 reference'; return 1; }
  [[ -d "${ACCEPTANCE_SOURCE_DIR:-}" && ! -L "${ACCEPTANCE_SOURCE_DIR:-}" ]] || { e2e_die 'ACCEPTANCE_SOURCE_DIR must be a real directory'; return 1; }
  [[ -f "${SPK_RAYJOB_CONFIG:-}" && ! -L "${SPK_RAYJOB_CONFIG:-}" ]] || { e2e_die 'SPK_RAYJOB_CONFIG must be a regular file'; return 1; }
  validate_tenant_namespace "${TENANT_NAMESPACE:-}" || return 1
  command -v jq >/dev/null || { e2e_die 'jq is required'; return 1; }
  command -v curl >/dev/null || { e2e_die 'curl is required for Portal API checks'; return 1; }
  command -v spk-rayjob >/dev/null || { e2e_die 'spk-rayjob is required'; return 1; }
  command -v perl >/dev/null || { e2e_die 'perl is required for bounded subprocesses'; return 1; }
  _secure_config_token "$SPK_RAYJOB_CONFIG" >/dev/null || return 1
}

_secure_config_token() {
  local config="$1" permissions credential_value
  [[ -f "$config" && ! -L "$config" ]] || { e2e_die 'session config must be a real file'; return 1; }
  permissions="$(stat -f '%Lp' "$config" 2>/dev/null || stat -c '%a' "$config")" || return
  [[ "$permissions" == 600 ]] || { e2e_die 'session config must be mode 0600'; return 1; }
  credential_value="$(jq -er '.token | select(type == "string")' "$config")" || { e2e_die 'session config token is malformed'; return 1; }
  [[ "${#credential_value}" -ge 16 && "${#credential_value}" -le 8192 ]] || { e2e_die 'session config has no bounded token'; return 1; }
  [[ "$credential_value" =~ ^[A-Za-z0-9._~+/=-]+$ ]] || { e2e_die 'Bearer token contains unsafe characters'; return 1; }
  printf '%s\n' "$credential_value"
}

portal_api_request() (
  local method="$1" path="$2" payload="${3:-}" idempotency_key="${4:-}" bearer_material config='' output
  [[ "$method" == GET || "$method" == POST ]] || { e2e_die 'unsupported Portal API method'; return 1; }
  [[ "$path" == /* && "$path" != *$'\n'* && "$path" != *$'\r'* ]] || { e2e_die 'unsafe Portal API path'; return 1; }
  bearer_material="$(_secure_config_token "${PORTAL_SESSION_CONFIG:?set PORTAL_SESSION_CONFIG}")" || return
  config="$(mktemp "${TMPDIR:-/tmp}/ray-train-portal.XXXXXXXX")" || return
  trap 'rm -f -- "$config"' EXIT HUP INT TERM
  chmod 600 "$config"
  printf 'silent\nshow-error\nfail-with-body\nconnect-timeout = 5\nmax-time = %s\nheader = "Authorization: Bearer %s"\n' "$E2E_COMMAND_TIMEOUT_SECONDS" "$bearer_material" >"$config"
  if [[ -n "$idempotency_key" ]]; then
    [[ "$idempotency_key" =~ ^[a-z0-9][a-z0-9-]{10,62}$ ]] || { e2e_die 'idempotency identity is unsafe'; return 1; }
    printf 'header = "Idempotency-Key: %s"\n' "$idempotency_key" >>"$config"
  fi
  if [[ -n "$payload" ]]; then
    if ! output="$(curl --config "$config" -X "$method" -H 'Content-Type: application/json' --data "$payload" "${API_URL%/}${path}")"; then
      return 1
    fi
  elif ! output="$(curl --config "$config" -X "$method" "${API_URL%/}${path}")"; then
    return 1
  fi
  printf '%s\n' "$output"
)

verify_interactive_portal_session() {
  local session auth_type
  session="$(portal_api_request GET '/api/v1/me')" || {
    e2e_die 'could not validate the Portal session before submission'
    return 1
  }
  auth_type="$(jq -er '.data.authType' <<<"$session")" || {
    e2e_die 'Portal session response has no authoritative authType'
    return 1
  }
  [[ "$auth_type" == oidc || "$auth_type" == local ]] || { e2e_die 'Portal acceptance requires an OIDC or local interactive session; PATs are forbidden'; return 1; }
}

build_portal_payload() {
  local name="$1" engine="$2" workers="$3" gpus="$4" mode="$5" parent_job_id="${6:-}" request entrypoint
  [[ -n "${PORTAL_SOURCE_SNAPSHOT_ID:-}" ]] || { e2e_die 'PORTAL_SOURCE_SNAPSHOT_ID is required for real Portal submission'; return 1; }
  entrypoint="$(entrypoint_for_engine "$engine")" || return
  request="$(jq -cn --arg name "$name" --arg image "$TRAINING_IMAGE" --arg snapshot "$PORTAL_SOURCE_SNAPSHOT_ID" --arg engine "$engine" --arg entrypoint "$entrypoint" --arg mode "$mode" --arg parent "$parent_job_id" --argjson workers "$workers" --argjson gpus "$gpus" '{spec:{name:$name,image:$image,source:{type:"workspace",snapshot:$snapshot},entrypoint:{command:($entrypoint|split(" "))},trainingEngine:$engine,execution:{mode:$mode},parentJobId:$parent,resources:{workerReplicas:$workers,gpusPerWorker:$gpus,cpuPerWorker:32,memoryPerWorker:"128Gi"},queue:"",output:{space:"my-runs",relativePath:("acceptance/"+$name)},timeoutSeconds:1800,cleanupPolicy:{successTtlSeconds:60,failureTtlSeconds:600}}} | if $engine == "ray-train" then .spec.managed={maxFailures:2,checkpoint:{everyEpochs:1,keepLatest:3,keepBest:1}} else . end')" || return
  printf '%s\n' "$request"
}

submit_portal_job() {
  local name="$1" engine="$2" workers="$3" gpus="$4" mode="$5" parent_job_id="${6:-}" request response
  request="$(build_portal_payload "$name" "$engine" "$workers" "$gpus" "$mode" "$parent_job_id")" || return
  response="$(portal_api_request POST '/api/v1/jobs' "$request" "$name")" || return
  jq -er '.data.id' <<<"$response"
}

source_request_id_for_submission() {
  local submission_name="$1" digest
  validate_acceptance_prefix "$ACCEPTANCE_PREFIX" || return
  [[ "$submission_name" == "${ACCEPTANCE_PREFIX}-"* ]] || { e2e_die 'source request must belong to the exact acceptance prefix'; return 1; }
  if command -v sha256sum >/dev/null; then
    digest="$(printf '%s' "$submission_name" | sha256sum)" || return
  elif command -v shasum >/dev/null; then
    digest="$(printf '%s' "$submission_name" | shasum -a 256)" || return
  else
    e2e_die 'sha256sum or shasum is required for stable source request identity'
    return 1
  fi
  digest="${digest%%[[:space:]]*}"
  [[ "$digest" =~ ^[0-9a-f]{64}$ ]] || { e2e_die 'could not derive a safe source request identity'; return 1; }
  printf 'source-request-%s\n' "${digest:0:24}"
}

reconcile_source_artifact_request() {
  local request_id="$1" response artifact_id
  [[ "$request_id" =~ ^source-request-[0-9a-f]{24}$ ]] || { e2e_die 'unsafe source request identity during reconciliation'; return 1; }
  response="$(bounded_command spk-rayjob source-artifact resolve --output json --config "$SPK_RAYJOB_CONFIG" --server "$API_URL" --request-id "$request_id")" || return
  artifact_id="$(jq -er '(.artifactId // .data.artifactId) | select(test("^artifact-[0-9a-f]{24}$"))' <<<"$response")" || {
    e2e_die 'source request reconciliation returned no safe server artifact ID'
    return 1
  }
  ledger_record sourceartifact "$artifact_id" "$TENANT_NAMESPACE" "$ACCEPTANCE_PREFIX" || return
}

submit_spk_job() {
  local name="$1" engine="$2" workers="$3" gpus="$4" mode="$5" parent_job_id="${6:-}" response entrypoint request_id submit_status
  entrypoint="$(entrypoint_for_engine "$engine")" || return
  request_id="$(source_request_id_for_submission "$name")" || return
  ledger_record sourceartifact-request "$request_id" "$TENANT_NAMESPACE" "$ACCEPTANCE_PREFIX" || return
  local args=(submit --output json --config "$SPK_RAYJOB_CONFIG" --server "$API_URL" --dir "$ACCEPTANCE_SOURCE_DIR" --source-request-id "$request_id" --name "$name" --image "$TRAINING_IMAGE" --entrypoint "$entrypoint" --engine "$engine" --workers "$workers" --gpus-per-worker "$gpus" --cpu-per-worker 32 --memory-per-worker 128Gi --execution-mode "$mode" --output-path "acceptance/$name")
  if [[ "$engine" == ray-train ]]; then
    args+=(--max-failures 2 --checkpoint-every-epochs 1 --checkpoint-keep-latest 3 --checkpoint-keep-best 1)
  fi
  [[ -z "$parent_job_id" ]] || args+=(--resume-from-job "$parent_job_id")
  if response="$(bounded_command spk-rayjob "${args[@]}")"; then
    :
  else
    submit_status=$?
    reconcile_source_artifact_request "$request_id" >/dev/null 2>&1 || true
    return "$submit_status"
  fi
  reconcile_source_artifact_request "$request_id" >/dev/null || return
  jq -er '.id // .data.id' <<<"$response"
}

submit_native_ray_job() (
  local name="$1" engine="$2" workers="$3" gpus="$4" _mode="$5" parent_job_id="${6:-}"
  [[ -z "$parent_job_id" ]] || { e2e_die 'native Ray resume is unsupported; submit the child through spk-rayjob'; return 1; }
  [[ "${RAY_CLI_IMAGE:-}" =~ ^[^@[:space:]]+@sha256:[0-9a-f]{64}$ ]] || { e2e_die 'RAY_CLI_IMAGE must be an immutable lowercase SHA-256 reference'; return 1; }
  command -v docker >/dev/null || { e2e_die 'docker is required for native Ray submission'; return 1; }
  local metadata env_file='' external_id platform_job deadline bearer_material entrypoint entrypoint_command entrypoint_file jobs matches match_count
  trap '[[ -z "$env_file" ]] || rm -f -- "$env_file"' EXIT HUP INT TERM
  external_id="${name//-/_}"
  metadata="$(jq -cn --arg image "$TRAINING_IMAGE" --arg workers "$workers" --arg gpus "$gpus" --arg engine "$engine" '{"ray-platform.image":$image,"ray-platform.worker-replicas":$workers,"ray-platform.gpus-per-worker":$gpus,"ray-platform.cpu-per-worker":"32","ray-platform.memory-per-worker":"128Gi","ray-platform.queue":"","platform.training.engine":$engine}')" || return
  entrypoint="$(entrypoint_for_engine "$engine")" || return
  read -r entrypoint_command entrypoint_file <<<"$entrypoint"
  [[ "$entrypoint_command" == python && "$entrypoint_file" =~ ^[a-z0-9_]+\.py$ ]] || { e2e_die 'native entrypoint is not the fixed acceptance Python script'; return 1; }
  bearer_material="$(_secure_config_token "$SPK_RAYJOB_CONFIG")" || return
  env_file="$(mktemp "${TMPDIR:-/tmp}/ray-train-native.XXXXXXXX")" || return
  chmod 600 "$env_file"
  printf 'RAY_ADDRESS=%s/ray\nRAY_JOB_HEADERS={"Authorization":"Bearer %s"}\n' "${API_URL%/}" "$bearer_material" >"$env_file"
  if ! bounded_command docker run --rm --env-file "$env_file" -v "$ACCEPTANCE_SOURCE_DIR:/source:ro" -w /source "$RAY_CLI_IMAGE" ray job submit --no-wait --submission-id "$external_id" --working-dir . --metadata-json "$metadata" -- "$entrypoint_command" "$entrypoint_file"; then
    return 1
  fi
  rm -f -- "$env_file"
  env_file=''
  deadline=$(( $(date +%s) + E2E_TIMEOUT_SECONDS ))
  platform_job=''
  while [[ -z "$platform_job" && "$(date +%s)" -lt "$deadline" ]]; do
    jobs="$(bounded_command spk-rayjob jobs --config "$SPK_RAYJOB_CONFIG" --server "$API_URL" --limit 500 --output json)" || return 1
    jq -e '(.items // .data.items) | type == "array"' <<<"$jobs" >/dev/null || { e2e_die 'native submission mapping returned malformed JSON'; return 1; }
    matches="$(jq -c --arg id "$external_id" '[((.items // .data.items)[]) | select(.externalSubmissionId == $id and .submissionOrigin == "ray-cli")]' <<<"$jobs")" || return
    match_count="$(jq 'length' <<<"$matches")" || return
    [[ "$match_count" -le 1 ]] || { e2e_die 'native submission identity mapped to multiple persisted jobs'; return 1; }
    if [[ "$match_count" -eq 1 ]]; then
      platform_job="$(jq -er '.[0].id' <<<"$matches")" || return
    fi
    [[ -n "$platform_job" ]] || sleep "$E2E_POLL_SECONDS"
  done
  [[ -n "$platform_job" ]] || { e2e_die 'native Ray submission did not create a persisted platform job'; return 1; }
  printf '%s\n' "$platform_job"
)

submission_identity_for_flow() {
  local flow="$1" name="$2"
  case "$flow" in
    portal|spk-rayjob) printf '%s\n' "$name" ;;
    native-ray) printf '%s\n' "${name//-/_}" ;;
    *) e2e_die "unsupported submission flow: $flow" ;;
  esac
}

reconcile_submission_intent() {
  local flow="$1" name="$2" identity expected_origin jobs matches count
  identity="$(submission_identity_for_flow "$flow" "$name")" || return
  case "$flow" in
    portal) expected_origin=portal ;;
    spk-rayjob|native-ray) expected_origin=ray-cli ;;
    *) e2e_die "unsupported reconciliation flow: $flow"; return 1 ;;
  esac
  jobs="$(bounded_command spk-rayjob jobs --config "$SPK_RAYJOB_CONFIG" --server "$API_URL" --limit 500 --output json)" || {
    e2e_die 'could not reconcile the persisted submission intent'
    return 1
  }
  jq -e '(.items // .data.items) | type == "array"' <<<"$jobs" >/dev/null || {
    e2e_die 'submission reconciliation returned malformed JSON'
    return 1
  }
  if [[ "$flow" == native-ray ]]; then
    matches="$(jq -c --arg identity "$identity" --arg origin "$expected_origin" --arg namespace "$TENANT_NAMESPACE" '[((.items // .data.items)[]) | select(.externalSubmissionId == $identity and .submissionOrigin == $origin and .kubernetesNamespace == $namespace)]' <<<"$jobs")" || return
  else
    matches="$(jq -c --arg name "$name" --arg origin "$expected_origin" --arg namespace "$TENANT_NAMESPACE" '[((.items // .data.items)[]) | select(.spec.name == $name and .submissionOrigin == $origin and .kubernetesNamespace == $namespace)]' <<<"$jobs")" || return
  fi
  count="$(jq 'length' <<<"$matches")" || return
  [[ "$count" -eq 1 ]] || {
    e2e_die "submission reconciliation found $count exact acceptance jobs"
    return 1
  }
  jq -er '.[0].id | select(test("^job-[0-9a-f]{24}$"))' <<<"$matches"
}

submit_acceptance_job() {
  local flow="$1" name="$2" identity result
  validate_acceptance_prefix "$ACCEPTANCE_PREFIX" || return 1
  [[ "$name" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ && "$name" == "${ACCEPTANCE_PREFIX}-"* ]] || {
    e2e_die 'submission name must preserve the exact acceptance prefix'
    return 1
  }
  identity="$(submission_identity_for_flow "$flow" "$name")" || return
  ledger_record submission-intent "$identity" "$TENANT_NAMESPACE" "$identity" || return
  case "$flow" in
    portal)
      if result="$(submit_portal_job "${@:2}")"; then printf '%s\n' "$result"; return 0; fi
      ;;
    spk-rayjob)
      if result="$(submit_spk_job "${@:2}")"; then printf '%s\n' "$result"; return 0; fi
      ;;
    native-ray)
      if result="$(submit_native_ray_job "${@:2}")"; then printf '%s\n' "$result"; return 0; fi
      ;;
    *) e2e_die "unsupported submission flow: $flow"; return 1 ;;
  esac
  reconcile_submission_intent "$flow" "$name"
}

job_status_json() {
  bounded_command spk-rayjob status --config "$SPK_RAYJOB_CONFIG" --server "$API_URL" --output json "$1"
}

wait_for_job() {
  local job_id="$1" deadline detail state
  deadline=$(( $(date +%s) + E2E_TIMEOUT_SECONDS ))
  while [[ "$(date +%s)" -lt "$deadline" ]]; do
    detail="$(job_status_json "$job_id")" || return
    state="$(jq -r '.observedState // .data.observedState // empty' <<<"$detail")" || return
    printf 'JOB_ID=%s STATE=%s\n' "$job_id" "$state" >&2
    case "$state" in
      SUCCEEDED) printf '%s\n' "$detail"; return 0 ;;
      FAILED|CANCELED|TIMED_OUT) printf '%s\n' "$detail" >&2; return 1 ;;
    esac
    sleep "$E2E_POLL_SECONDS"
  done
  e2e_die "bounded timeout waiting for job $job_id"
}

verify_persisted_job() {
  local detail="$1" expected_engine="$2" expected_version="$3" expected_origin="$4" expected_workers="$5" expected_gpus="$6"
  jq -e --arg engine "$expected_engine" --arg version "$expected_version" --arg origin "$expected_origin" --argjson workers "$expected_workers" --argjson gpus "$expected_gpus" '(.data // .) as $job | $job.submissionOrigin == $origin and $job.spec.trainingEngine == $engine and $job.spec.rayVersion == $version and $job.spec.resources.workerReplicas == $workers and $job.spec.resources.gpusPerWorker == $gpus' <<<"$detail" >/dev/null || { e2e_die 'persisted engine/rayVersion/origin/topology does not match the submitted case'; return 1; }
}

verify_acceptance_identity() {
  local detail="$1" flow="$2" expected_name="$3" external_id
  validate_acceptance_prefix "$ACCEPTANCE_PREFIX" || return
  [[ "$expected_name" == "${ACCEPTANCE_PREFIX}-"* ]] || { e2e_die 'submitted name lost its acceptance prefix'; return 1; }
  case "$flow" in
    portal|spk-rayjob)
      jq -e --arg name "$expected_name" '(.data // .).spec.name == $name' <<<"$detail" >/dev/null || { e2e_die 'persisted job name lost its acceptance identity'; return 1; }
      ;;
    native-ray)
      external_id="${expected_name//-/_}"
      jq -e --arg id "$external_id" '(.data // .).externalSubmissionId == $id' <<<"$detail" >/dev/null || { e2e_die 'native persisted job lost its acceptance submission identity'; return 1; }
      ;;
    *) e2e_die "unsupported identity flow: $flow" ;;
  esac
}

record_persisted_resources() {
  local detail="$1" job_id namespace ray_job ray_cluster source_artifact
  job_id="$(jq -er '(.data // .).id' <<<"$detail")" || return
  namespace="$(jq -er '(.data // .).kubernetesNamespace' <<<"$detail")" || return
  ray_job="$(jq -er '(.data // .).rayJobName' <<<"$detail")" || return
  ray_cluster="$(jq -er '(.data // .).rayClusterName' <<<"$detail")" || return
  source_artifact="$(jq -r '(.data // .).sourceArtifactId // empty' <<<"$detail")" || return
  ledger_record rayjob "$ray_job" "$namespace" "$job_id" || return
  ledger_record raycluster "$ray_cluster" "$namespace" "$job_id" || return
  if [[ -n "$source_artifact" ]]; then
    ledger_record sourceartifact "$source_artifact" "$namespace" "$job_id" || return
  fi
}

legacy_e2e_main() {
  acceptance_setup
  printf 'ACCEPTANCE_PREFIX=%s\n' "$ACCEPTANCE_PREFIX"
  if ! e2e_live_enabled; then
    echo 'DRY_RUN legacy training harness made no submission'
    return 0
  fi
  : "${API_URL:?set API_URL}"
  : "${IMAGE:?set IMAGE}"
  : "${GIT_URL:?set GIT_URL}"
  : "${GIT_COMMIT:?set GIT_COMMIT}"
  local access_token="${ACCESS_TOKEN:-}" allow_anonymous="${ALLOW_ANONYMOUS:-false}"
  [[ -n "$access_token" || "$allow_anonymous" == true ]] || e2e_die 'set ACCESS_TOKEN or explicitly set ALLOW_ANONYMOUS=true'
  echo 'Legacy e2e-training entrypoint remains available; Task 16 acceptance uses the dedicated harnesses.'
  exec python3 "${E2E_ROOT_DIR}/scripts/e2e_training.py"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  legacy_e2e_main "$@"
fi
