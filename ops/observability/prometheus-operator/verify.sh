#!/usr/bin/env bash
set -euo pipefail

readonly kubectl_bin="${KUBECTL_BIN:-kubectl}"
readonly namespace='monitoring'

for required in "$kubectl_bin" jq; do
  command -v "$required" >/dev/null || { echo "missing required command: ${required}" >&2; exit 1; }
done

for crd in servicemonitors.monitoring.coreos.com prometheuses.monitoring.coreos.com alertmanagers.monitoring.coreos.com; do
  "$kubectl_bin" get crd "$crd" >/dev/null
done

"$kubectl_bin" -n "$namespace" rollout status deployment/prometheus-operator --timeout=10m
"$kubectl_bin" -n "$namespace" rollout status deployment/prometheus-grafana --timeout=10m
"$kubectl_bin" -n "$namespace" rollout status statefulset/prometheus-prometheus-prometheus --timeout=10m
"$kubectl_bin" -n "$namespace" rollout status statefulset/alertmanager-prometheus-alertmanager --timeout=10m
"$kubectl_bin" -n "$namespace" get servicemonitor dcgm-exporter >/dev/null
"$kubectl_bin" -n "$namespace" wait --for=jsonpath='{.status.availableReplicas}'=2 prometheus/prometheus-prometheus --timeout=10m

grafana_pod="$("$kubectl_bin" -n "$namespace" get pods \
  -l app.kubernetes.io/name=grafana,app.kubernetes.io/instance=prometheus \
  --field-selector=status.phase=Running \
  -o jsonpath='{.items[0].metadata.name}')"
[[ -n "$grafana_pod" ]] || { echo 'no running Grafana pod available for Prometheus query validation' >&2; exit 1; }

dcgm_query="$("$kubectl_bin" -n "$namespace" exec "$grafana_pod" -c grafana -- \
  curl -fsS 'http://prometheus-prometheus:9090/api/v1/query?query=count%28DCGM_FI_DEV_GPU_UTIL%29')"
echo "$dcgm_query" | jq -e '
  .status == "success" and
  (.data.result | length) == 1 and
  (.data.result[0].value[1] | tonumber) > 0
' >/dev/null

echo 'Prometheus Operator, Grafana, two Prometheus replicas and DCGM GPU metrics are ready'
