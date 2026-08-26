#!/usr/bin/env bash
set -euo pipefail

readonly ops_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly root_dir="$(cd -- "${ops_dir}/../../.." && pwd)"
readonly chart_dir="${root_dir}/helm/ray-cache-local"
readonly data1_profile="${chart_dir}/values-vke-data1.yaml"
readonly data2_profile="${chart_dir}/values-vke-data2.yaml"
readonly namespace="ray-cache-local"

[[ "$#" -eq 0 ]] || { echo 'install.sh accepts no overrides' >&2; exit 2; }
for command in helm kubectl; do
  command -v "${command}" >/dev/null || { echo "missing command: ${command}" >&2; exit 1; }
done

bash "${chart_dir}/tests/capacity-contract.sh"
bash "${chart_dir}/tests/render-contract.sh"
bash "${chart_dir}/tests/dual-render-contract.sh"
bash "${chart_dir}/tests/monitor-contract.sh"
bash "${ops_dir}/test/ops-contract-test.sh"
helm lint "${chart_dir}" --values "${data1_profile}"
helm lint "${chart_dir}" --values "${data2_profile}"
bash "${ops_dir}/preflight.sh"

helm upgrade --install ray-cache-local-data1 "${chart_dir}" \
  --namespace "${namespace}" \
  --create-namespace \
  --values "${data1_profile}" \
  --atomic \
  --wait \
  --timeout 10m

helm upgrade --install ray-cache-local-data2 "${chart_dir}" \
  --namespace "${namespace}" \
  --create-namespace \
  --values "${data2_profile}" \
  --atomic \
  --wait \
  --timeout 10m

bash "${ops_dir}/verify-dual.sh"
