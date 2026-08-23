#!/usr/bin/env bash
set -euo pipefail

readonly chart_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly production_values="${chart_dir}/values-vke-production.yaml"

[[ -f "${chart_dir}/Chart.yaml" ]] || { echo 'missing ray-cache-local Chart.yaml' >&2; exit 1; }
command -v helm >/dev/null || { echo 'missing command: helm' >&2; exit 1; }

default_rendered="$(mktemp)"
production_rendered="$(mktemp)"
trap 'rm -f "${default_rendered}" "${production_rendered}"' EXIT

helm lint "${chart_dir}" >/dev/null
helm lint "${chart_dir}" --values "${production_values}" >/dev/null
helm template ray-cache-local "${chart_dir}" --namespace ray-cache-local >"${default_rendered}"
helm template ray-cache-local "${chart_dir}" --namespace ray-cache-local \
  --values "${production_values}" >"${production_rendered}"

require() {
  local file="$1"
  local expected="$2"
  grep -Fq -- "${expected}" "${file}" || {
    echo "render contract missing: ${expected}" >&2
    exit 1
  }
}

reject() {
  local file="$1"
  local forbidden="$2"
  if grep -Fq -- "${forbidden}" "${file}"; then
    echo "render contract contains forbidden value: ${forbidden}" >&2
    exit 1
  fi
}

require "${production_rendered}" 'provisioner: rancher.io/local-path'
require "${production_rendered}" 'name: ray-cache-local'
require "${production_rendered}" 'reclaimPolicy: Delete'
require "${production_rendered}" 'volumeBindingMode: WaitForFirstConsumer'
require "${production_rendered}" 'allowVolumeExpansion: false'
require "${production_rendered}" 'storageclass.kubernetes.io/is-default-class: "false"'
require "${production_rendered}" '"node": "172.28.1.232"'
require "${production_rendered}" '"node": "172.28.1.233"'
require "${production_rendered}" '/data1/ray-cache'
require "${production_rendered}" '/data2/ray-cache'
require "${production_rendered}" '"node": "DEFAULT_PATH_FOR_NON_LISTED_NODES"'
require "${production_rendered}" '"paths": []'
require "${production_rendered}" 'harbor.wellspiking.ai/guofeng.su/local-path-provisioner@sha256:7d30aa9da00db4d40dbe3d2bb41a0fb6a3a926cbe915908458b0ec470db73115'
require "${production_rendered}" 'harbor.wellspiking.ai/guofeng.su/busybox@sha256:ff6bba6f18535e7ccb3c1bbed0b84e5c733d7d9dd8815f1ea93ee73073135aa4'

# Portable defaults must not know any production node identity.
reject "${default_rendered}" '172.28.1.232'
reject "${default_rendered}" '172.28.1.233'

# Production path maps are an exact allowlist: two GPU nodes plus the deny-all default.
map_nodes="$(grep -F '"node":' "${production_rendered}" | sed -E 's/^.*"node": "([^"]+)".*$/\1/' | sort -u)"
expected_nodes=$'172.28.1.232\n172.28.1.233\nDEFAULT_PATH_FOR_NON_LISTED_NODES'
[[ "${map_nodes}" == "${expected_nodes}" ]] || {
  echo "unexpected nodePathMap nodes:" >&2
  printf '%s\n' "${map_nodes}" >&2
  exit 1
}

reject "${production_rendered}" 'docker.io/'
reject "${production_rendered}" 'quay.io/'
reject "${production_rendered}" 'registry.k8s.io/'
reject "${production_rendered}" ':latest'
reject "${production_rendered}" 'allowUnsafePathPattern'
reject "${production_rendered}" '"setupCommand"'
reject "${production_rendered}" '"teardownCommand"'
reject "${production_rendered}" '"pathPattern"'
reject "${production_rendered}" 'add: ["CHOWN"'
require "${production_rendered}" 'kind: Role'
require "${production_rendered}" 'resources: ["pods"]'
require "${production_rendered}" 'resources: ["pods/log"]'

# Never expose the disk roots themselves as provisioner paths.
if grep -Eq '^[[:space:]]*-[[:space:]]*/data[12][[:space:]]*$' "${production_rendered}"; then
  echo 'broad NVMe root exposed in nodePathMap' >&2
  exit 1
fi

# Provisioner RBAC must not expose secrets.
reject "${production_rendered}" 'resources: ["secrets"]'
reject "${production_rendered}" 'resources: [secrets]'

echo 'ray cache local render contract verified'
