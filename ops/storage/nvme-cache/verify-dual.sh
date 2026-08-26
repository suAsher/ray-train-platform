#!/usr/bin/env bash
set -euo pipefail

readonly ops_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
RAY_CACHE_VERIFY_LIBRARY_ONLY=1
export RAY_CACHE_VERIFY_LIBRARY_ONLY
# shellcheck source=verify.sh
source "${ops_dir}/verify.sh"
unset RAY_CACHE_VERIFY_LIBRARY_ONLY

readonly namespace="ray-cache-local"
readonly nodes=(172.28.1.232 172.28.1.233)
readonly ssh_options=(-o BatchMode=yes -o ConnectTimeout=10)
readonly suffix="$(date +%s)-$((RANDOM % 65536))"

current_pod=''
current_pvc1=''
current_pvc2=''
manifest=''

cleanup() {
  [[ -z "${current_pod}" ]] || kubectl -n "${namespace}" delete pod "${current_pod}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  [[ -z "${current_pvc1}" ]] || kubectl -n "${namespace}" delete pvc "${current_pvc1}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  [[ -z "${current_pvc2}" ]] || kubectl -n "${namespace}" delete pvc "${current_pvc2}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  [[ -z "${manifest}" || ! -f "${manifest}" ]] || rm -f -- "${manifest}"
}
trap cleanup EXIT

for command in kubectl ssh sed mktemp; do
  command -v "${command}" >/dev/null || { echo "missing command: ${command}" >&2; exit 1; }
done
kubectl get storageclass ray-cache-local-data1 ray-cache-local-data2 >/dev/null

for node in "${nodes[@]}"; do
  current_pod="ray-cache-dual-${suffix}-${node##*.}"
  current_pvc1="${current_pod}-data1"
  current_pvc2="${current_pod}-data2"
  manifest="$(mktemp)"
  sed -e "s/__POD_NAME__/${current_pod}/g" \
      -e "s/__PVC1_NAME__/${current_pvc1}/g" \
      -e "s/__PVC2_NAME__/${current_pvc2}/g" \
      -e "s/__NODE_NAME__/${node}/g" \
      "${ops_dir}/smoke-pod-dual.yaml" >"${manifest}"

  kubectl apply -f "${manifest}"
  kubectl -n "${namespace}" wait pod "${current_pod}" --for=condition=Ready --timeout=5m
  kubectl -n "${namespace}" exec "${current_pod}" -- test -s /mnt/cache/marker
  kubectl -n "${namespace}" exec "${current_pod}" -- test -s /mnt/cache2/marker

  pv1="$(kubectl -n "${namespace}" get pvc "${current_pvc1}" -o jsonpath='{.spec.volumeName}')"
  pv2="$(kubectl -n "${namespace}" get pvc "${current_pvc2}" -o jsonpath='{.spec.volumeName}')"
  path1="$(kubectl get pv "${pv1}" -o jsonpath='{.spec.local.path}{.spec.hostPath.path}')"
  path2="$(kubectl get pv "${pv2}" -o jsonpath='{.spec.local.path}{.spec.hostPath.path}')"
  validate_local_path "${path1}"
  validate_local_path "${path2}"
  [[ "${path1}" == /data1/ray-cache/* && "${path2}" == /data2/ray-cache/* ]] || {
    echo "dual cache roots are wrong: ${path1} ${path2}" >&2
    exit 1
  }
  [[ "$(kubectl -n "${namespace}" get pod "${current_pod}" -o jsonpath='{.spec.nodeName}')" == "${node}" ]]

  kubectl -n "${namespace}" delete pod "${current_pod}" --wait=true
  current_pod=''
  kubectl -n "${namespace}" delete pvc "${current_pvc1}" "${current_pvc2}" --wait=true
  current_pvc1=''
  current_pvc2=''
  for pv in "${pv1}" "${pv2}"; do
    for _ in {1..60}; do
      kubectl get pv "${pv}" >/dev/null 2>&1 || break
      sleep 2
    done
    kubectl get pv "${pv}" >/dev/null 2>&1 && { echo "PV ${pv} was not deleted" >&2; exit 1; }
  done
  remote_path_absent "${node}" "${path1}"
  remote_path_absent "${node}" "${path2}"
  rm -f -- "${manifest}"
  manifest=''
done

echo 'dual NVMe provisioning, co-location, write and cleanup verified'
