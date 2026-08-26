#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly PREFLIGHT="${ROOT_DIR}/ops/platform/preflight.sh"
readonly TEMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TEMP_DIR"' EXIT

cat >"${TEMP_DIR}/profile.yaml" <<'EOF'
namespace: ray-train-platform
EOF

cat >"${TEMP_DIR}/helm" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  lint)
    exit 0
    ;;
  template)
    cat <<YAML
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ray-train-backend
spec:
  template:
    spec:
      containers:
        - name: backend
          env:
            - name: TRAINING_NODE_SELECTOR
              value: "accelerator=nvidia-rtx-4090"
YAML
    if [[ "${MOCK_CACHE_RENDER:-present}" == "present" ]]; then
      cat <<YAML
            - name: LOCAL_CACHE_ENABLED
              value: "${MOCK_CACHE_ENABLED:-false}"
            - name: LOCAL_CACHE_STORAGE_CLASS_DATA1
              value: "${MOCK_CACHE_STORAGE_CLASS_DATA1:-}"
            - name: LOCAL_CACHE_STORAGE_CLASS_DATA2
              value: "${MOCK_CACHE_STORAGE_CLASS_DATA2:-}"
YAML
    fi
    ;;
  *)
    echo "unexpected helm invocation: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "${TEMP_DIR}/helm"

cat >"${TEMP_DIR}/kubectl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >>"${MOCK_KUBECTL_LOG}"

case "$*" in
  "get crd rayjobs.ray.io"|\
  "get crd rayclusters.ray.io"|\
  "get crd clusterqueues.kueue.x-k8s.io"|\
  "get crd localqueues.kueue.x-k8s.io")
    exit 0
    ;;
  "get nodes -l accelerator=nvidia-rtx-4090 -o name")
    echo 'node/gpu-1'
    exit 0
    ;;
  "get nodes -l accelerator=nvidia-rtx-4090 -o jsonpath={range .items[*]}{.status.allocatable.nvidia\\.com/gpu}{\"\\n\"}{end}")
    echo '8'
    exit 0
    ;;
  "get storageclass ray-cache-local-data1 -o jsonpath={.provisioner}")
    [[ "${MOCK_SC_EXISTS:-true}" == "true" ]] || exit 1
    printf '%s' "${MOCK_SC_PROVISIONER:-wellspiking.ai/local-path-data1}"
    exit 0
    ;;
  "get storageclass ray-cache-local-data2 -o jsonpath={.provisioner}")
    [[ "${MOCK_SC_EXISTS:-true}" == "true" ]] || exit 1
    printf '%s' "${MOCK_SC_PROVISIONER_DATA2:-wellspiking.ai/local-path-data2}"
    exit 0
    ;;
  "get storageclass ray-cache-local-data1 -o jsonpath={.volumeBindingMode}"|\
  "get storageclass ray-cache-local-data2 -o jsonpath={.volumeBindingMode}")
    printf '%s' "${MOCK_SC_BINDING:-WaitForFirstConsumer}"
    exit 0
    ;;
  "get storageclass ray-cache-local-data1 -o jsonpath={.reclaimPolicy}"|\
  "get storageclass ray-cache-local-data2 -o jsonpath={.reclaimPolicy}")
    printf '%s' "${MOCK_SC_RECLAIM:-Delete}"
    exit 0
    ;;
  "get storageclass ray-cache-local-data1 -o jsonpath={.metadata.annotations.storageclass\\.kubernetes\\.io/is-default-class}"|\
  "get storageclass ray-cache-local-data2 -o jsonpath={.metadata.annotations.storageclass\\.kubernetes\\.io/is-default-class}")
    printf '%s' "${MOCK_SC_DEFAULT:-false}"
    exit 0
    ;;
  "get storageclass ray-cache-local-data1 -o jsonpath={.metadata.annotations.storageclass\\.beta\\.kubernetes\\.io/is-default-class}"|\
  "get storageclass ray-cache-local-data2 -o jsonpath={.metadata.annotations.storageclass\\.beta\\.kubernetes\\.io/is-default-class}")
    printf '%s' "${MOCK_SC_BETA_DEFAULT:-false}"
    exit 0
    ;;
  "get storageclass ray-cache-local-data1 -o jsonpath={.allowVolumeExpansion}"|\
  "get storageclass ray-cache-local-data2 -o jsonpath={.allowVolumeExpansion}")
    printf '%s' "${MOCK_SC_EXPANSION:-false}"
    exit 0
    ;;
  "-n ray-cache-local get deployment ray-cache-local-data1 -o jsonpath={.metadata.labels.app\\.kubernetes\\.io/instance}")
    [[ "${MOCK_DEPLOYMENT_EXISTS:-true}" == "true" ]] || exit 1
    printf '%s' "${MOCK_RELEASE_LABEL:-ray-cache-local-data1}"
    exit 0
    ;;
  "-n ray-cache-local get deployment ray-cache-local-data2 -o jsonpath={.metadata.labels.app\\.kubernetes\\.io/instance}")
    [[ "${MOCK_DEPLOYMENT_EXISTS:-true}" == "true" ]] || exit 1
    printf '%s' "${MOCK_RELEASE_LABEL_DATA2:-ray-cache-local-data2}"
    exit 0
    ;;
  "-n ray-cache-local get deployment ray-cache-local-data1 -o jsonpath={.status.conditions[?(@.type==\"Available\")].status}"|\
  "-n ray-cache-local get deployment ray-cache-local-data2 -o jsonpath={.status.conditions[?(@.type==\"Available\")].status}")
    printf '%s' "${MOCK_AVAILABLE_STATUS:-True}"
    exit 0
    ;;
  "-n ray-train-platform get secret ray-platform-postgres"|\
  "-n ray-train-platform get secret ray-platform-pat"|\
  "-n ray-train-platform get secret ray-platform-bootstrap-admin")
    exit 1
    ;;
  *)
    echo "unexpected kubectl invocation: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "${TEMP_DIR}/kubectl"

run_case() {
  local name="$1"
  local expected_status="$2"
  local expected_message="$3"
  shift 3

  local log_file="${TEMP_DIR}/${name}.kubectl.log"
  local output status
  : >"${log_file}"
  if output="$(env \
    MOCK_KUBECTL_LOG="${log_file}" \
    HELM="${TEMP_DIR}/helm" \
    KUBECTL="${TEMP_DIR}/kubectl" \
    "$@" \
    bash "${PREFLIGHT}" --profile "${TEMP_DIR}/profile.yaml" 2>&1)"; then
    status=0
  else
    status=$?
  fi

  if [[ "${expected_status}" == success && "${status}" -ne 0 ]]; then
    echo "${name}: expected success, got exit ${status}" >&2
    echo "${output}" >&2
    exit 1
  fi
  if [[ "${expected_status}" == failure && "${status}" -eq 0 ]]; then
    echo "${name}: expected failure" >&2
    exit 1
  fi
  if [[ -n "${expected_message}" && "${output}" != *"${expected_message}"* ]]; then
    echo "${name}: missing diagnostic: ${expected_message}" >&2
    echo "${output}" >&2
    exit 1
  fi

  CASE_LOG="${log_file}"
}

assert_cache_checks_skipped() {
  local name="$1"
  if grep -Eq 'storageclass|ray-cache-local' "${CASE_LOG}"; then
    echo "${name}: disabled or unavailable cache must skip StorageClass and provisioner checks" >&2
    cat "${CASE_LOG}" >&2
    exit 1
  fi
}

run_case disabled success 'preflight passed' \
  MOCK_CACHE_ENABLED=false MOCK_CACHE_STORAGE_CLASS_DATA1=ray-cache-local-data1 MOCK_CACHE_STORAGE_CLASS_DATA2=ray-cache-local-data2
assert_cache_checks_skipped disabled

run_case unavailable success 'preflight passed' MOCK_CACHE_RENDER=absent
assert_cache_checks_skipped unavailable

run_case enabled-good success 'preflight passed' \
  MOCK_CACHE_ENABLED=true MOCK_CACHE_STORAGE_CLASS_DATA1=ray-cache-local-data1 MOCK_CACHE_STORAGE_CLASS_DATA2=ray-cache-local-data2
grep -Fq 'get storageclass ray-cache-local-data1' "${CASE_LOG}"
grep -Fq 'get storageclass ray-cache-local-data2' "${CASE_LOG}"
grep -Fq -- '-n ray-cache-local get deployment ray-cache-local-data1 -o jsonpath={.status.conditions[?(@.type=="Available")].status}' "${CASE_LOG}"
grep -Fq -- '-n ray-cache-local get deployment ray-cache-local-data2 -o jsonpath={.status.conditions[?(@.type=="Available")].status}' "${CASE_LOG}"
if grep -Eq '(^| )(apply|create|delete|patch|replace|edit)( |$)' "${CASE_LOG}"; then
  echo 'local cache platform preflight must remain read-only' >&2
  cat "${CASE_LOG}" >&2
  exit 1
fi

run_case enabled-empty-class failure 'LOCAL_CACHE_STORAGE_CLASS is empty' \
  MOCK_CACHE_ENABLED=true MOCK_CACHE_STORAGE_CLASS_DATA1= MOCK_CACHE_STORAGE_CLASS_DATA2=ray-cache-local-data2
run_case missing-class failure 'local cache StorageClass is absent: ray-cache-local-data1' \
  MOCK_CACHE_ENABLED=true MOCK_CACHE_STORAGE_CLASS_DATA1=ray-cache-local-data1 MOCK_CACHE_STORAGE_CLASS_DATA2=ray-cache-local-data2 MOCK_SC_EXISTS=false
run_case wrong-provisioner failure 'expected wellspiking.ai/local-path-data1' \
  MOCK_CACHE_ENABLED=true MOCK_CACHE_STORAGE_CLASS_DATA1=ray-cache-local-data1 MOCK_CACHE_STORAGE_CLASS_DATA2=ray-cache-local-data2 MOCK_SC_PROVISIONER=example.invalid/provisioner
run_case wrong-binding failure 'expected WaitForFirstConsumer' \
  MOCK_CACHE_ENABLED=true MOCK_CACHE_STORAGE_CLASS_DATA1=ray-cache-local-data1 MOCK_CACHE_STORAGE_CLASS_DATA2=ray-cache-local-data2 MOCK_SC_BINDING=Immediate
run_case wrong-reclaim failure 'expected Delete' \
  MOCK_CACHE_ENABLED=true MOCK_CACHE_STORAGE_CLASS_DATA1=ray-cache-local-data1 MOCK_CACHE_STORAGE_CLASS_DATA2=ray-cache-local-data2 MOCK_SC_RECLAIM=Retain
run_case default-class failure 'must not be default via storageclass.kubernetes.io/is-default-class' \
  MOCK_CACHE_ENABLED=true MOCK_CACHE_STORAGE_CLASS_DATA1=ray-cache-local-data1 MOCK_CACHE_STORAGE_CLASS_DATA2=ray-cache-local-data2 MOCK_SC_DEFAULT=true
run_case beta-default-class failure 'must not be default via storageclass.beta.kubernetes.io/is-default-class' \
  MOCK_CACHE_ENABLED=true MOCK_CACHE_STORAGE_CLASS_DATA1=ray-cache-local-data1 MOCK_CACHE_STORAGE_CLASS_DATA2=ray-cache-local-data2 MOCK_SC_BETA_DEFAULT=true
run_case expansion-enabled failure 'must not allow volume expansion' \
  MOCK_CACHE_ENABLED=true MOCK_CACHE_STORAGE_CLASS_DATA1=ray-cache-local-data1 MOCK_CACHE_STORAGE_CLASS_DATA2=ray-cache-local-data2 MOCK_SC_EXPANSION=true
run_case wrong-release failure 'expected Helm release ray-cache-local-data1' \
  MOCK_CACHE_ENABLED=true MOCK_CACHE_STORAGE_CLASS_DATA1=ray-cache-local-data1 MOCK_CACHE_STORAGE_CLASS_DATA2=ray-cache-local-data2 MOCK_RELEASE_LABEL=other-release
run_case missing-deployment failure 'cache provisioner Deployment is absent: ray-cache-local/ray-cache-local-data1' \
  MOCK_CACHE_ENABLED=true MOCK_CACHE_STORAGE_CLASS_DATA1=ray-cache-local-data1 MOCK_CACHE_STORAGE_CLASS_DATA2=ray-cache-local-data2 MOCK_DEPLOYMENT_EXISTS=false
run_case unready-deployment failure 'cache provisioner Deployment is not Ready: ray-cache-local/ray-cache-local-data1' \
  MOCK_CACHE_ENABLED=true MOCK_CACHE_STORAGE_CLASS_DATA1=ray-cache-local-data1 MOCK_CACHE_STORAGE_CLASS_DATA2=ray-cache-local-data2 MOCK_AVAILABLE_STATUS=False

echo 'platform local cache preflight contract verified'
