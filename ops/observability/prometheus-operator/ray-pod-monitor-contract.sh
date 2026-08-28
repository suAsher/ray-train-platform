#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly manifest="${root_dir}/ops/observability/prometheus-operator/60-ray-pod-monitor.yaml"

[[ -f "${manifest}" ]] || { echo 'Ray PodMonitor manifest is missing' >&2; exit 1; }
for expected in \
  'kind: PodMonitor' \
  'any: true' \
  'port: metrics' \
  'values: [head, worker]' \
  'values: [ray-train-platform]' \
  '__meta_kubernetes_pod_label_platform_job_id' \
  '__meta_kubernetes_namespace' \
  'exported_namespace' \
  '__meta_kubernetes_pod_label_ray_io_cluster' \
  'ray_io_cluster' \
  '__meta_kubernetes_pod_label_ray_io_node_type' \
  'ray_io_node_type' \
  '__meta_kubernetes_pod_name' \
  'targetLabel: pod' \
  '__meta_kubernetes_pod_node_name' \
  'targetLabel: node'; do
  grep -Fq -- "${expected}" "${manifest}" || { echo "Ray PodMonitor missing: ${expected}" >&2; exit 1; }
done

echo 'Ray PodMonitor contract verified'
