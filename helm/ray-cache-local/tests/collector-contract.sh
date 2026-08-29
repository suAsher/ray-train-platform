#!/usr/bin/env bash
set -euo pipefail

readonly chart_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
temporary="$(mktemp -d)"
trap 'rm -rf "${temporary}"' EXIT

root1="${temporary}/data1"
root2="${temporary}/data2"
textfile="${temporary}/textfile"
eligible="${root1}/pvc-01234567-89ab-cdef-0123-456789abcdef_tenant-a_cache-job-01"
mkdir -p "${eligible}/.ray-cache-metrics" "${root2}" "${textfile}"
cat >"${eligible}/.ray-cache-metrics/preload.metrics" <<'EOF'
version=1
platform_job_id=job-01
exported_namespace=tenant-a
ray_io_cluster=job-01-cluster
ray_io_node_type=worker
pod=job-01-cluster-worker-abc
bytes=12
files=2
seconds=0.250000
copied=1
hits=3
misses=1
EOF

malicious="${root1}/pvc-fedcba98-7654-3210-fedc-ba9876543210_tenant-a_cache-evil"
mkdir -p "${malicious}/.ray-cache-metrics"
cat >"${malicious}/.ray-cache-metrics/preload.metrics" <<'EOF'
version=1
platform_job_id=job"} or vector(1)
exported_namespace=tenant-a
ray_io_cluster=cluster-a
ray_io_node_type=worker
pod=cluster-a-worker-evil
bytes=999
files=1
seconds=1
copied=1
hits=0
misses=1
EOF

RUN_ONCE=1 CACHE_ROOT_DATA1="${root1}" CACHE_ROOT_DATA2="${root2}" TEXTFILE_DIR="${textfile}" \
  sh "${chart_dir}/files/collect-cache-metrics"

output="${textfile}/ray_cache.prom"
grep -Fq 'ray_cache_bytes{platform_job_id="job-01",exported_namespace="tenant-a",ray_io_cluster="job-01-cluster",ray_io_node_type="worker",pod="job-01-cluster-worker-abc"} 12' "${output}"
grep -Fq 'ray_cache_hits_total{platform_job_id="job-01"' "${output}"
grep -Fq 'ray_cache_misses_total{platform_job_id="job-01"' "${output}"
grep -Fq 'ray_cache_preloader_duration_seconds{platform_job_id="job-01"' "${output}"
if grep -Fq '999' "${output}" || grep -Fq 'vector' "${output}"; then
  echo 'collector trusted unsafe metric data' >&2
  exit 1
fi

echo 'ray cache collector contract verified'
