#!/usr/bin/env bash
set -euo pipefail

if [[ "${CONFIRM_LEGACY_CLEANUP:-}" != 'DELETE' ]]; then
  echo 'set CONFIRM_LEGACY_CLEANUP=DELETE only after the new prometheus release is verified' >&2
  exit 2
fi

readonly kubectl_bin="${KUBECTL_BIN:-kubectl}"
readonly helm_bin="${HELM_BIN:-helm}"
readonly namespace='monitoring'

"$kubectl_bin" -n "$namespace" get prometheus prometheus-prometheus >/dev/null
"$kubectl_bin" -n "$namespace" get servicemonitor dcgm-exporter >/dev/null

"$helm_bin" -n "$namespace" uninstall ray-observability --wait
"$kubectl_bin" -n "$namespace" delete pvc \
  -l 'app.kubernetes.io/name=ray-observability,app.kubernetes.io/component=prometheus' \
  --ignore-not-found
"$kubectl_bin" -n "$namespace" delete configmap ray-grafana-dashboard-provisioning ray-grafana-dashboard-json --ignore-not-found
"$kubectl_bin" -n "$namespace" delete secret ray-platform-grafana-admin --ignore-not-found

echo 'legacy native observability stack removed; two 200Gi Prometheus PVCs were released'
