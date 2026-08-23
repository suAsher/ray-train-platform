#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly chart_dir="${root_dir}/helm/ray-cache-local"
readonly ops_dir="${root_dir}/ops/storage/nvme-cache"
readonly production_values="${chart_dir}/values-vke-production.yaml"
readonly delivery_render_test="${root_dir}/scripts/test-delivery-render.sh"

grep -Fq -- 'test-nvme-cache-delivery.sh' "${delivery_render_test}" || {
  echo 'NVMe cache delivery test is not integrated into test-delivery-render.sh' >&2
  exit 1
}

required_files=(
  "${chart_dir}/Chart.yaml"
  "${chart_dir}/values.yaml"
  "${production_values}"
  "${chart_dir}/files/setup"
  "${chart_dir}/files/teardown"
  "${chart_dir}/files/collect-cache-metrics"
  "${chart_dir}/templates/monitor-daemonset.yaml"
  "${chart_dir}/templates/servicemonitor.yaml"
  "${chart_dir}/templates/prometheusrule.yaml"
  "${ops_dir}/install.sh"
  "${ops_dir}/preflight.sh"
  "${ops_dir}/verify.sh"
  "${ops_dir}/register-node.sh"
  "${ops_dir}/uninstall.sh"
  "${ops_dir}/smoke-pod.yaml"
)
for required_file in "${required_files[@]}"; do
  [[ -f "${required_file}" ]] || { echo "missing delivery file: ${required_file}" >&2; exit 1; }
done

for command in helm grep bash; do
  command -v "${command}" >/dev/null || { echo "missing command: ${command}" >&2; exit 1; }
done

helm lint "${chart_dir}" >/dev/null
helm lint "${chart_dir}" --values "${production_values}" >/dev/null
bash "${chart_dir}/tests/render-contract.sh"
bash "${chart_dir}/tests/capacity-contract.sh"
bash "${chart_dir}/tests/monitor-contract.sh"
bash "${ops_dir}/test/ops-contract-test.sh"

shell_files=("${BASH_SOURCE[0]}")
while IFS= read -r -d '' script; do
  shell_files+=("${script}")
done < <(find "${chart_dir}/files" "${chart_dir}/tests" "${ops_dir}" -type f \
  \( -name '*.sh' -o -name setup -o -name teardown -o -name collect-cache-metrics \) -print0)
for script in "${shell_files[@]}"; do
  bash -n "${script}"
  if [[ "$(head -n 1 "${script}")" == '#!/bin/sh' ]]; then
    sh -n "${script}"
  fi
done

if command -v shellcheck >/dev/null; then
  shellcheck "${shell_files[@]}"
else
  echo 'shellcheck not installed; skipped'
fi

echo 'NVMe cache infrastructure delivery verified'
