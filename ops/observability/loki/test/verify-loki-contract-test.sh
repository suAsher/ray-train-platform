#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../../.." && pwd)"
readonly verify_script="${root_dir}/ops/observability/loki/30-verify-loki.sh"
readonly deploy_script="${root_dir}/ops/observability/loki/deploy-ha.sh"
readonly values="${root_dir}/ops/observability/loki/20-values-cpu-ha.yaml"

for required in "$verify_script" "$deploy_script"; do
  [[ -f "$required" ]] || { echo "missing Loki operation tool: ${required}" >&2; exit 1; }
done

grep -Fq 'LOKI_RELEASE:-loki-cpu' "$verify_script"
grep -Fq 'LOKI_RELEASE:-loki-cpu' "$deploy_script"
grep -Fq 'app.kubernetes.io/component=single-binary,app.kubernetes.io/instance=${release}' "$verify_script"
grep -Fq 'wait --for=condition=Ready pod' "$verify_script"
grep -Fq 'preferredDuringSchedulingIgnoredDuringExecution:' "$values"
grep -Fq 'weight: 100' "$values"
grep -Fq 'key: platform.wellspiking.ai/pool' "$values"
grep -Fq 'values: [control-plane]' "$values"
grep -Fq 'key: node.kubernetes.io/instance-type' "$values"
grep -Fq 'values: [virtual-node]' "$values"
if grep -A3 'nodeSelector:' "$values" | grep -Fq 'platform.wellspiking.ai/pool: control-plane'; then
  echo 'Loki must not retain a hard control-plane nodeSelector' >&2
  exit 1
fi
if grep -Fq 'nvidia.com/gpu' "$values"; then
  echo 'Loki must not request GPU resources' >&2
  exit 1
fi

echo 'Loki verification defaults and readiness contract verified'
