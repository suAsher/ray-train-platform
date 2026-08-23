#!/usr/bin/env bash
set -euo pipefail

node=''
output_dir=''
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --node)
      [[ "$#" -ge 2 ]] || { echo '--node requires a value' >&2; exit 2; }
      node="$2"
      shift 2
      ;;
    --output-dir)
      [[ "$#" -ge 2 ]] || { echo '--output-dir requires a value' >&2; exit 2; }
      output_dir="$2"
      shift 2
      ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

[[ "${node}" =~ ^[A-Za-z0-9.-]+$ ]] || { echo 'node must be a DNS name or IP address' >&2; exit 2; }
case "${node}" in
  172.28.1.232|172.28.1.233) echo "node ${node} is already registered" >&2; exit 2 ;;
esac
readonly suffix="$(date +%s)-$((RANDOM % 65536))"
if [[ -z "${output_dir}" ]]; then
  output_dir="${PWD}/nvme-cache-node-${node}-${suffix}"
fi
readonly cache_roots=(/data1/ray-cache /data2/ray-cache)
readonly ssh_options=(-o BatchMode=yes -o ConnectTimeout=10)
active_probe=''
staging_dir=''
patch_temp=''
report_temp=''

cleanup() {
  if [[ -n "${active_probe}" ]]; then
    ssh "${ssh_options[@]}" "root@${node}" \
      "rm -f '${active_probe}/marker'; rmdir '${active_probe}'" >/dev/null 2>&1 || true
  fi
  if [[ -n "${staging_dir}" && -d "${staging_dir}" && ! -L "${staging_dir}" ]]; then
    [[ -z "${patch_temp}" ]] || rm -f -- "${patch_temp}"
    [[ -z "${report_temp}" ]] || rm -f -- "${report_temp}"
    rmdir -- "${staging_dir}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

for command in kubectl ssh jq awk date mkdir mktemp mv rmdir; do
  command -v "${command}" >/dev/null || { echo "missing command: ${command}" >&2; exit 1; }
done

if [[ -e "${output_dir}" || -L "${output_dir}" ]]; then
  echo "output directory already exists or is a symlink: ${output_dir}" >&2
  exit 2
fi
mkdir -m 0700 -- "${output_dir}"
readonly patch_file="${output_dir}/values-patch.yaml"
readonly report_file="${output_dir}/acceptance-report.txt"
staging_dir="$(mktemp -d "${output_dir}/.staging.XXXXXX")"
patch_temp="${staging_dir}/values-patch.yaml"
report_temp="${staging_dir}/acceptance-report.txt"
set -o noclobber

publish_file() {
  local source_file="$1"
  local target_file="$2"
  [[ ! -e "${target_file}" && ! -L "${target_file}" ]] || {
    echo "refusing to overwrite output file: ${target_file}" >&2
    return 1
  }
  mv -n -- "${source_file}" "${target_file}"
  [[ ! -e "${source_file}" && -f "${target_file}" ]] || {
    echo "atomic output publish failed without overwriting: ${target_file}" >&2
    return 1
  }
}

node_json="$(kubectl get node "${node}" -o json)"
jq -e '.status.conditions[] | select(.type == "Ready" and .status == "True")' <<<"${node_json}" >/dev/null
jq -e '.metadata.labels.accelerator == "nvidia-rtx-4090" and .metadata.labels["gpu-pool"] == "production"' \
  <<<"${node_json}" >/dev/null

{
  printf 'node=%s\n' "${node}"
  printf 'checked_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'ready=true\nlabels=accelerator=nvidia-rtx-4090,gpu-pool=production\n'
} >"${report_temp}"

for cache_root in "${cache_roots[@]}"; do
  probe="${cache_root}/.ray-cache-register-${suffix}"
  active_probe="${probe}"
  ssh "${ssh_options[@]}" "root@${node}" \
    "set -eu; test -d '${cache_root}'; test ! -L '${cache_root}'; install -d -m 0770 '${probe}'; printf 'probe\\n' > '${probe}/marker'; test -s '${probe}/marker'; rm -f '${probe}/marker'; rmdir '${probe}'"
  mount_source="$(ssh "${ssh_options[@]}" "root@${node}" "findmnt -n -o SOURCE --target '${cache_root}'")"
  free_percent="$(ssh "${ssh_options[@]}" "root@${node}" \
    "df -Pk '${cache_root}' | awk 'END { printf \"%.2f\", (\$4 * 100 / \$2) }'")"
  printf 'path=%s mount_source=%s free_percent=%s write_delete_smoke=passed\n' \
    "${cache_root}" "${mount_source}" "${free_percent}" >>"${report_temp}"
  active_probe=''
done

cat >"${patch_temp}" <<EOF
nodePathMap:
  - node: "172.28.1.232"
    paths:
      - /data1/ray-cache
      - /data2/ray-cache
  - node: "172.28.1.233"
    paths:
      - /data1/ray-cache
      - /data2/ray-cache
  - node: "${node}"
    paths:
      - /data1/ray-cache
      - /data2/ray-cache
EOF

publish_file "${report_temp}" "${report_file}"
publish_file "${patch_temp}" "${patch_file}"
rmdir -- "${staging_dir}"
staging_dir=''

printf 'generated review-only values patch: %s\n' "${patch_file}"
printf 'generated acceptance report: %s\n' "${report_file}"
printf 'No Helm release or Kubernetes resource was changed.\n'
