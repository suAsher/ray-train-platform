#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../../.." && pwd)"
readonly verify_script="${root_dir}/ops/observability/loki/30-verify-loki.sh"
readonly deploy_script="${root_dir}/ops/observability/loki/deploy-ha.sh"

for required in "$verify_script" "$deploy_script"; do
  [[ -f "$required" ]] || { echo "missing Loki operation tool: ${required}" >&2; exit 1; }
done

grep -Fq 'LOKI_RELEASE:-loki-cpu' "$verify_script"
grep -Fq 'LOKI_RELEASE:-loki-cpu' "$deploy_script"
grep -Fq 'app.kubernetes.io/component=single-binary,app.kubernetes.io/instance=${release}' "$verify_script"
grep -Fq 'wait --for=condition=Ready pod' "$verify_script"

echo 'Loki verification defaults and readiness contract verified'
