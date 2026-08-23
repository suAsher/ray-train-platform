#!/usr/bin/env bash
set -euo pipefail

readonly chart_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly production_values="${chart_dir}/values-vke-production.yaml"

for required in \
  templates/monitor-daemonset.yaml \
  templates/monitor-service.yaml \
  templates/servicemonitor.yaml \
  templates/prometheusrule.yaml \
  files/collect-cache-metrics; do
  [[ -f "${chart_dir}/${required}" ]] || { echo "missing ${required}" >&2; exit 1; }
done
command -v helm >/dev/null || { echo 'missing command: helm' >&2; exit 1; }

default_rendered="$(mktemp)"
production_rendered="$(mktemp)"
trap 'rm -f "${default_rendered}" "${production_rendered}"' EXIT

helm template ray-cache-local "${chart_dir}" --namespace ray-cache-local >"${default_rendered}"
helm template ray-cache-local "${chart_dir}" --namespace ray-cache-local \
  --values "${production_values}" >"${production_rendered}"

require() {
  local expected="$1"
  grep -Fq -- "${expected}" "${production_rendered}" || {
    echo "monitor contract missing: ${expected}" >&2
    exit 1
  }
}

reject() {
  local file="$1"
  local forbidden="$2"
  if grep -Fq -- "${forbidden}" "${file}"; then
    echo "monitor contract contains forbidden value: ${forbidden}" >&2
    exit 1
  fi
}

# Optional CRD-backed resources and the monitor are off in portable defaults.
reject "${default_rendered}" 'kind: DaemonSet'
reject "${default_rendered}" 'kind: ServiceMonitor'
reject "${default_rendered}" 'kind: PrometheusRule'

require 'kind: DaemonSet'
require 'name: ray-cache-local-monitor'
require 'accelerator: nvidia-rtx-4090'
require 'gpu-pool: production'
require 'hostPath:'
require 'path: /data1/ray-cache'
require 'path: /data2/ray-cache'
require 'mountPath: /host/data1-ray-cache'
require 'mountPath: /host/data2-ray-cache'
require 'readOnly: true'
require 'automountServiceAccountToken: false'
require 'privileged: false'
require 'runAsNonRoot: true'
require 'runAsUser: 65534'
require 'runAsGroup: 0'
require 'requests:'
require 'cpu: 25m'
require 'memory: 32Mi'
require 'cpu: 50m'
require 'memory: 64Mi'
require 'harbor.wellspiking.ai/guofeng.su/node-exporter@sha256:8c9bac11973b94b59be88d6e11fee4429aa743c8846cdc75d65b18db33f6a106'
require 'harbor.wellspiking.ai/guofeng.su/busybox@sha256:ff6bba6f18535e7ccb3c1bbed0b84e5c733d7d9dd8815f1ea93ee73073135aa4'
require 'value: "30"'
require 'sleep "${interval_seconds}"'
require 'ray_cache_filesystem_size_bytes'
require 'ray_cache_filesystem_available_bytes'
require 'ray_cache_volume_directories'
require 'ray_cache_teardown_failures_total'
require 'pvc-[0-9a-f]'
require 'kind: ServiceMonitor'
require 'kind: PrometheusRule'
require 'RayCacheFilesystemUsageWarning'
require 'RayCacheFilesystemUsageHigh'
require 'RayCacheFilesystemUsageCritical'
require 'RayCacheProvisionerUnavailable'
require 'absent(kube_deployment_status_replicas_available'
require 'RayCachePVCProvisioningPending'
require 'RayCacheTeardownFailure'
require '>= 0.75'
require '>= 0.85'
require '>= 0.92'

reject "${production_rendered}" 'nvidia.com/gpu'
reject "${production_rendered}" 'privileged: true'
reject "${production_rendered}" 'automountServiceAccountToken: true'
reject "${production_rendered}" 'docker.io/'
reject "${production_rendered}" 'quay.io/'
reject "${production_rendered}" 'kubectl delete'
reject "${production_rendered}" 'kubectl cordon'
reject "${production_rendered}" 'DAC_READ_SEARCH'
reject "${production_rendered}" 'add: ['
if grep -Eq '^[[:space:]]+(path: /data[12]|mountPath: /host/data[12])$' "${production_rendered}"; then
  echo 'monitor must not mount whole NVMe disks' >&2
  exit 1
fi

echo 'ray cache monitor contract verified'
