#!/usr/bin/env bash
set -euo pipefail

validate_local_path() {
  local local_path="$1"
  local path_pattern='^/data[12]/ray-cache/pvc-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}_[a-z0-9]([-a-z0-9]*[a-z0-9])?_[a-z0-9]([-a-z0-9.]*[a-z0-9])?$'

  [[ "${local_path}" =~ ${path_pattern} ]] || {
    echo "unsafe local cache path: ${local_path}" >&2
    return 1
  }
  [[ "${local_path}" != *'..'* ]] || {
    echo "local cache path contains traversal syntax: ${local_path}" >&2
    return 1
  }
}

remote_path_absent() {
  local node="$1"
  local local_path="$2"
  local remote_path_check="sh -ceu 'test ! -e \"\$1\"' sh"

  validate_local_path "${local_path}" || return 1
  ssh "${ssh_options[@]}" "root@${node}" "${remote_path_check}" "${local_path}"
}

if [[ "${RAY_CACHE_VERIFY_LIBRARY_ONLY:-}" == 1 ]]; then
  return 0 2>/dev/null || exit 0
fi

readonly ops_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly namespace="ray-cache-local"
readonly storage_class="ray-cache-local"
readonly nodes=(172.28.1.232 172.28.1.233)
readonly ssh_options=(-o BatchMode=yes -o ConnectTimeout=10)
readonly suffix="$(date +%s)-$((RANDOM % 65536))"

current_pod=''
current_pvc=''
current_pv=''
manifest=''

cleanup() {
  if [[ -n "${current_pod}" ]]; then
    kubectl --namespace "${namespace}" delete pod "${current_pod}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  fi
  if [[ -n "${current_pvc}" ]]; then
    kubectl --namespace "${namespace}" delete pvc "${current_pvc}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  fi
  if [[ -n "${manifest}" && -f "${manifest}" ]]; then
    rm -f "${manifest}"
  fi
}
trap cleanup EXIT

for command in kubectl ssh sed grep mktemp; do
  command -v "${command}" >/dev/null || { echo "missing command: ${command}" >&2; exit 1; }
done

kubectl get storageclass "${storage_class}" >/dev/null

for node in "${nodes[@]}"; do
  current_pod="ray-cache-smoke-${suffix}-${node##*.}"
  current_pvc="ray-cache-smoke-${suffix}-${node##*.}"
  current_pv=''
  manifest="$(mktemp)"
  sed \
    -e "s/__POD_NAME__/${current_pod}/g" \
    -e "s/__PVC_NAME__/${current_pvc}/g" \
    -e "s/__NODE_NAME__/${node}/g" \
    "${ops_dir}/smoke-pod.yaml" >"${manifest}"

  kubectl apply -f "${manifest}"
  kubectl --namespace "${namespace}" wait pod "${current_pod}" --for=condition=Ready --timeout=5m
  actual_node="$(kubectl --namespace "${namespace}" get pod "${current_pod}" -o jsonpath='{.spec.nodeName}')"
  [[ "${actual_node}" == "${node}" ]] || { echo "smoke pod landed on ${actual_node}, expected ${node}" >&2; exit 1; }
  kubectl --namespace "${namespace}" exec "${current_pod}" -- test -s /mnt/cache/marker

  current_pv="$(kubectl --namespace "${namespace}" get pvc "${current_pvc}" -o jsonpath='{.spec.volumeName}')"
  [[ -n "${current_pv}" ]] || { echo "PVC ${current_pvc} has no PV" >&2; exit 1; }
  local_path="$(kubectl get pv "${current_pv}" -o jsonpath='{.spec.local.path}{.spec.hostPath.path}')"
  validate_local_path "${local_path}" || { echo "PV ${current_pv} has unsafe local path" >&2; exit 1; }
  kubectl get pv "${current_pv}" -o yaml | grep -Fq -- "${node}" || {
    echo "PV ${current_pv} lacks node affinity for ${node}" >&2
    exit 1
  }

  kubectl --namespace "${namespace}" delete pod "${current_pod}" --wait=true
  current_pod=''
  kubectl --namespace "${namespace}" delete pvc "${current_pvc}" --wait=true
  current_pvc=''

  pv_deleted=false
  for _ in {1..60}; do
    if ! kubectl get pv "${current_pv}" >/dev/null 2>&1; then
      pv_deleted=true
      break
    fi
    sleep 2
  done
  [[ "${pv_deleted}" == true ]] || { echo "PV ${current_pv} was not deleted" >&2; exit 1; }
  remote_path_absent "${node}" "${local_path}" || {
    echo "host cache directory remains: ${node}:${local_path}" >&2
    exit 1
  }

  rm -f "${manifest}"
  manifest=''
  current_pv=''
done

echo 'NVMe cache provisioning, affinity, write, deletion, and host cleanup verified'
