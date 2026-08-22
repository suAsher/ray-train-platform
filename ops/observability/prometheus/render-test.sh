#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly helm_bin="${HELM_BIN:-helm}"
readonly chart_dir="${root_dir}/helm/ray-observability"

rendered="$("$helm_bin" template ray-observability "$chart_dir" --namespace monitoring)"
for expected in \
  'kind: StatefulSet' \
  'name: ray-observability-prometheus' \
  'kind: Deployment' \
  'name: ray-observability-grafana' \
  'role: endpoints' \
  'dcgm-exporter' \
  'storageClassName: "ebs-ssd"' \
  'harbor.wellspiking.ai/hub/prom/prometheus@sha256:23031bfe0e74a13004252caaa74eccd0d62b6c6e7a04711d5b8bf5b7e113adc7'; do
  grep -Fq "$expected" <<<"$rendered" || { echo "rendered observability chart missing ${expected}" >&2; exit 1; }
done

echo 'native Prometheus/Grafana production chart rendered successfully'
