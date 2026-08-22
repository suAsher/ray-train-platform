#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly kubectl_bin="${KUBECTL_BIN:-kubectl}"
readonly helm_bin="${HELM_BIN:-helm}"
readonly namespace='monitoring'
readonly release_name='prometheus'
readonly image_pull_secret="${IMAGE_PULL_SECRET:-harbor-registry}"
readonly image_pull_secret_source_namespace="${IMAGE_PULL_SECRET_SOURCE_NAMESPACE:-ray-train-platform}"
readonly chart="${root_dir}/helm/vendor/kube-prometheus-stack-87.18.1.tgz"
readonly values="${root_dir}/ops/observability/prometheus-operator/20-values-production.yaml"
readonly dashboard="${root_dir}/ops/observability/prometheus-operator/50-gpu-training-dashboard.yaml"
readonly dcgm_service_monitor="${root_dir}/ops/observability/prometheus-operator/40-dcgm-service-monitor.yaml"

for required in "$kubectl_bin" "$helm_bin" openssl jq; do
  command -v "$required" >/dev/null || { echo "missing required command: ${required}" >&2; exit 1; }
done
[[ -f "$chart" && -f "$values" && -f "$dashboard" && -f "$dcgm_service_monitor" ]] || {
  echo 'Prometheus Operator deployment assets are incomplete' >&2
  exit 1
}

"${root_dir}/ops/observability/prometheus-operator/render-test.sh"
"$kubectl_bin" create namespace "$namespace" --dry-run=client -o yaml | "$kubectl_bin" apply -f -

# Kubernetes Secrets are namespace-scoped. Reuse the existing Harbor pull
# credential without ever printing its contents. For another cluster, set
# IMAGE_PULL_SECRET_SOURCE_NAMESPACE or pre-create IMAGE_PULL_SECRET in
# monitoring before executing this script.
if ! "$kubectl_bin" -n "$namespace" get secret "$image_pull_secret" >/dev/null 2>&1; then
  "$kubectl_bin" -n "$image_pull_secret_source_namespace" get secret "$image_pull_secret" -o json \
    | jq --arg namespace "$namespace" 'del(.metadata.namespace, .metadata.uid, .metadata.resourceVersion, .metadata.creationTimestamp, .metadata.managedFields) | .metadata.namespace = $namespace' \
    | "$kubectl_bin" -n "$namespace" apply -f -
fi

if ! "$kubectl_bin" -n "$namespace" get secret grafana-admin >/dev/null 2>&1; then
  password="$(openssl rand -base64 36 | tr -d '\n' | tr -d '/+=' | cut -c1-32)"
  "$kubectl_bin" -n "$namespace" create secret generic grafana-admin \
    --from-literal=admin-user=admin \
    --from-literal=admin-password="$password"
  echo 'created Grafana administrator credential; retrieve it only with authorized kubectl Secret access' >&2
fi

"$helm_bin" upgrade --install "$release_name" "$chart" \
  --namespace "$namespace" \
  --values "$values" \
  --atomic --wait --timeout 20m

"$kubectl_bin" -n "$namespace" apply -f "$dashboard"
"$kubectl_bin" -n "$namespace" apply -f "$dcgm_service_monitor"
"${root_dir}/ops/observability/prometheus-operator/verify.sh"
