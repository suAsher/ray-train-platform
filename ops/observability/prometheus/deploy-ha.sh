#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly kubectl_bin="${KUBECTL_BIN:-kubectl}"
readonly helm_bin="${HELM_BIN:-helm}"
readonly namespace='monitoring'
readonly release_name='ray-observability'
readonly chart="${root_dir}/helm/ray-observability"
readonly values_file="${root_dir}/ops/observability/prometheus/20-values-cpu-ha.yaml"
readonly dashboard_provider="${root_dir}/ops/observability/grafana/20-dashboard-provisioning.yaml"
readonly dashboard_json="${root_dir}/ops/observability/grafana/20-dashboard-ray-training.json"

for required in "$kubectl_bin" "$helm_bin" openssl; do
  command -v "$required" >/dev/null || { echo "missing required command: ${required}" >&2; exit 1; }
done

[[ -d "$chart" && -f "$values_file" && -f "$dashboard_provider" && -f "$dashboard_json" ]] || {
  echo 'observability deployment assets are incomplete' >&2
  exit 1
}

"$kubectl_bin" create namespace "$namespace" --dry-run=client -o yaml | "$kubectl_bin" apply -f -
if ! "$kubectl_bin" -n "$namespace" get secret ray-platform-grafana-admin >/dev/null 2>&1; then
  password="$(openssl rand -base64 36 | tr -d '\n' | tr -d '/+=' | cut -c1-32)"
  "$kubectl_bin" -n "$namespace" create secret generic ray-platform-grafana-admin \
    --from-literal=admin-user=admin \
    --from-literal=admin-password="$password"
  echo 'created Grafana administrator credential; retrieve it only with authorized kubectl Secret access' >&2
fi

"$kubectl_bin" -n "$namespace" create configmap ray-grafana-dashboard-provisioning \
  --from-file=dashboard-provider.yaml="$dashboard_provider" \
  --dry-run=client -o yaml | "$kubectl_bin" apply -f -
"$kubectl_bin" -n "$namespace" create configmap ray-grafana-dashboard-json \
  --from-file=ray-training-platform.json="$dashboard_json" \
  --dry-run=client -o yaml | "$kubectl_bin" apply -f -

"$helm_bin" upgrade --install "$release_name" "$chart" \
  --namespace "$namespace" \
  --values "$values_file" \
  --atomic --wait --timeout 12m

"${root_dir}/ops/observability/prometheus/verify.sh"
