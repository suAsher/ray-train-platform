#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly chart_dir="${root_dir}/helm/ray-observability"
readonly values_file="${root_dir}/ops/observability/prometheus/20-values-cpu-ha.yaml"
readonly release_name='ray-observability'
readonly namespace='monitoring'
readonly kubectl_bin="${KUBECTL_BIN:-kubectl}"

require_values_contract() {
  [[ -f "$values_file" ]] || { echo "missing production Prometheus values: ${values_file}" >&2; return 1; }
  local expected
  for expected in \
    'replicas: 2' \
    'retention: 90d' \
    'className: ebs-ssd' \
    'harbor.wellspiking.ai/hub/prom/prometheus@sha256:' \
    'harbor.wellspiking.ai/hub/grafana/grafana@sha256:'; do
    grep -Fq "$expected" "$values_file" || { echo "Prometheus values missing ${expected}" >&2; return 1; }
  done
}

if [[ "${1:-}" == '--render-only' ]]; then
  require_values_contract
  "${root_dir}/ops/observability/prometheus/render-test.sh"
  exit 0
fi

require_values_contract
"${root_dir}/ops/observability/prometheus/render-test.sh"
"$kubectl_bin" -n "$namespace" rollout status statefulset/${release_name}-prometheus --timeout=8m
"$kubectl_bin" -n "$namespace" rollout status deployment/${release_name}-grafana --timeout=8m

target_url="http://127.0.0.1:9090/api/v1/query?query=DCGM_FI_DEV_GPU_UTIL"
prometheus_pod="$("$kubectl_bin" -n "$namespace" get pods -l app.kubernetes.io/name=ray-observability,app.kubernetes.io/component=prometheus -o jsonpath='{.items[0].metadata.name}')"
[[ -n "$prometheus_pod" ]] || { echo 'no Prometheus Pod found' >&2; exit 1; }
result="$("$kubectl_bin" -n "$namespace" exec "$prometheus_pod" -- /bin/busybox wget -qO- "$target_url")"
grep -Fq '"status":"success"' <<<"$result" || { echo 'Prometheus DCGM query failed' >&2; exit 1; }
grep -Fq 'DCGM_FI_DEV_GPU_UTIL' <<<"$result" || { echo 'Prometheus has no DCGM GPU-utilisation series' >&2; exit 1; }

echo 'Prometheus/Grafana production observability verified'
