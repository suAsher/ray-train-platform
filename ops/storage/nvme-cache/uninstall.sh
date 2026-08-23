#!/usr/bin/env bash
set -euo pipefail

readonly release_name="ray-cache-local"
readonly namespace="ray-cache-local"
readonly storage_class="ray-cache-local"
confirm_empty=false

if [[ "$#" -gt 1 ]]; then
  echo 'usage: uninstall.sh [--confirm-empty]' >&2
  exit 2
fi
if [[ "$#" -eq 1 ]]; then
  [[ "$1" == '--confirm-empty' ]] || { echo 'usage: uninstall.sh [--confirm-empty]' >&2; exit 2; }
  confirm_empty=true
fi

for command in helm kubectl jq; do
  command -v "${command}" >/dev/null || { echo "missing command: ${command}" >&2; exit 1; }
done

pvc_json="$(kubectl get pvc --all-namespaces -o json)"
pv_json="$(kubectl get pv -o json)"

echo 'ray-cache-local PVCs:'
jq -r --arg sc "${storage_class}" \
  '.items[] | select(.spec.storageClassName == $sc) | [.metadata.namespace, .metadata.name, .status.phase] | @tsv' \
  <<<"${pvc_json}"
echo 'ray-cache-local PVs:'
jq -r --arg sc "${storage_class}" \
  '.items[] | select(.spec.storageClassName == $sc) | [.metadata.name, .status.phase, (.spec.claimRef.namespace // "-"), (.spec.claimRef.name // "-")] | @tsv' \
  <<<"${pv_json}"

pvc_count="$(jq --arg sc "${storage_class}" '[.items[] | select(.spec.storageClassName == $sc)] | length' <<<"${pvc_json}")"
pv_count="$(jq --arg sc "${storage_class}" '[.items[] | select(.spec.storageClassName == $sc)] | length' <<<"${pv_json}")"
if [[ "${pvc_count}" -ne 0 || "${pv_count}" -ne 0 ]]; then
  echo "refusing uninstall: ${pvc_count} PVC(s) and ${pv_count} PV(s) still reference ${storage_class}" >&2
  exit 1
fi

if [[ "${confirm_empty}" != true ]]; then
  echo 'storage is empty; rerun with --confirm-empty to uninstall the release' >&2
  exit 2
fi

helm uninstall "${release_name}" --namespace "${namespace}" --wait
echo 'release removed; host cache roots were not deleted'
