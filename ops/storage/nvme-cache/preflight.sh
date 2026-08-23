#!/usr/bin/env bash
set -euo pipefail

readonly nodes=(172.28.1.232 172.28.1.233)
readonly cache_roots=(/data1/ray-cache /data2/ray-cache)
readonly label_contracts=(accelerator=nvidia-rtx-4090 gpu-pool=production)
readonly provisioner_image='harbor.wellspiking.ai/guofeng.su/local-path-provisioner@sha256:7d30aa9da00db4d40dbe3d2bb41a0fb6a3a926cbe915908458b0ec470db73115'
readonly helper_image='harbor.wellspiking.ai/guofeng.su/busybox@sha256:ff6bba6f18535e7ccb3c1bbed0b84e5c733d7d9dd8815f1ea93ee73073135aa4'
readonly monitor_image='harbor.wellspiking.ai/guofeng.su/node-exporter@sha256:8c9bac11973b94b59be88d6e11fee4429aa743c8846cdc75d65b18db33f6a106'
readonly ssh_options=(-o BatchMode=yes -o ConnectTimeout=10)

for command in kubectl ssh jq awk; do
  command -v "${command}" >/dev/null || { echo "missing command: ${command}" >&2; exit 1; }
done

kubectl get crd servicemonitors.monitoring.coreos.com >/dev/null
kubectl get crd prometheusrules.monitoring.coreos.com >/dev/null

# Pulling an exact repository@digest proves registry authentication, remote
# retrieval, and digest resolution. This may populate the container runtime
# image cache on each target node, but preflight never starts containers.
echo 'preflight image verification may populate the container runtime image cache; it never starts containers'

for node in "${nodes[@]}"; do
  node_json="$(kubectl get node "${node}" -o json)"
  jq -e '.status.conditions[] | select(.type == "Ready" and .status == "True")' <<<"${node_json}" >/dev/null
  for label_contract in "${label_contracts[@]}"; do
    label_key="${label_contract%%=*}"
    label_value="${label_contract#*=}"
    jq -e --arg key "${label_key}" --arg value "${label_value}" \
      '.metadata.labels[$key] == $value' <<<"${node_json}" >/dev/null || {
        echo "node ${node} missing ${label_contract}" >&2
        exit 1
      }
  done

  source1="$(ssh "${ssh_options[@]}" "root@${node}" 'findmnt -n -o SOURCE --target /data1')"
  source2="$(ssh "${ssh_options[@]}" "root@${node}" 'findmnt -n -o SOURCE --target /data2')"
  [[ -n "${source1}" && -n "${source2}" && "${source1}" != "${source2}" ]] || {
    echo "node ${node} does not expose two independent NVMe mounts" >&2
    exit 1
  }

  for cache_root in "${cache_roots[@]}"; do
    ssh "${ssh_options[@]}" "root@${node}" \
      "test -d '${cache_root}' && test -w '${cache_root}' && test ! -L '${cache_root}'"
    root_mode="$(ssh "${ssh_options[@]}" "root@${node}" "stat -c '%a' '${cache_root}'")"
    [[ "${root_mode}" == 770 ]] || { echo "${node}:${cache_root} must have mode 0770" >&2; exit 1; }
    ssh "${ssh_options[@]}" "root@${node}" \
      "df -Pk '${cache_root}' | awk 'END { exit !(\$2 > 0 && (\$4 * 100 / \$2) > 20) }'"
  done

  for image in "${provisioner_image}" "${helper_image}" "${monitor_image}"; do
    ssh "${ssh_options[@]}" "root@${node}" "crictl pull '${image}' >/dev/null" || {
      echo "node ${node} cannot pull exact pinned image ${image}" >&2
      exit 1
    }
  done
done

echo 'NVMe cache preflight verified two production GPU nodes'
