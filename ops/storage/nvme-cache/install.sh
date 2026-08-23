#!/usr/bin/env bash
set -euo pipefail

readonly ops_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly root_dir="$(cd -- "${ops_dir}/../../.." && pwd)"
readonly chart_dir="${root_dir}/helm/ray-cache-local"
readonly profile="${chart_dir}/values-vke-production.yaml"
readonly release_name="ray-cache-local"
readonly namespace="ray-cache-local"

[[ "$#" -eq 0 ]] || { echo 'install.sh accepts no overrides' >&2; exit 2; }
for command in helm kubectl; do
  command -v "${command}" >/dev/null || { echo "missing command: ${command}" >&2; exit 1; }
done

bash "${chart_dir}/tests/capacity-contract.sh"
bash "${chart_dir}/tests/render-contract.sh"
bash "${chart_dir}/tests/monitor-contract.sh"
bash "${ops_dir}/test/ops-contract-test.sh"
helm lint "${chart_dir}" --values "${profile}"
bash "${ops_dir}/preflight.sh"

helm upgrade --install "${release_name}" "${chart_dir}" \
  --namespace "${namespace}" \
  --create-namespace \
  --values "${profile}" \
  --atomic \
  --wait \
  --timeout 10m
