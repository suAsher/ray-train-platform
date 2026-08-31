#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
runner="$root_dir/ops/datasets/run-s1h-index-build.sh"

fail() {
  printf 'S1H index build runner contract: %s\n' "$1" >&2
  exit 1
}

[[ -x "$runner" ]] || fail 'run-s1h-index-build.sh must be executable'
grep -Fq 'platform.wellspiking.ai/gpu-pool' "$runner" || fail 'large CPU job must target the production worker pool'
grep -Fq 'runAsNonRoot' "$runner" || fail 'job must enforce a non-root runtime'
grep -Fq 'readOnlyRootFilesystem' "$runner" || fail 'job root filesystem must be read-only'
grep -Fq 'automountServiceAccountToken' "$runner" || fail 'job must not receive a Kubernetes API token'
grep -Fq 'data-public' "$runner" || fail 'job must discover the governed public claim'
grep -Fq 'generate_s1h_public_indexes.py' "$runner" || fail 'job must generate fresh PKLs'
grep -Fq 'build_s1h_trusted_index.py' "$runner" || fail 'job must generate the trusted index'
grep -Fq 'trusted-index-v2.pkl' "$runner" || fail 'job must publish the platform index contract'
grep -Fq 'trusted-index-v2.parts' "$runner" || fail 'job must publish bounded content-addressed index parts'
grep -Fq 'manifest root committed last' "$runner" || fail 'job must commit the root manifest after every part'
grep -Fq -- '--run-id' "$runner" || fail 'job must support an auditable retry without changing the dataset version'
grep -Fq -- '--finalize-only' "$runner" || fail 'job must support resuming a validated build at publication'
grep -Fq 'source metadata changed during index generation' \
  "$root_dir/examples/bevfusion/scripts/generate_s1h_public_indexes.py" ||
  fail 'job generator must reject a changing source tree'

if grep -Eqi '(nvidia.com/gpu|secretName|AKLT|SecretAccessKey|glpat-|rpt_)' "$runner"; then
  fail 'CPU index job must not request GPUs or embed credentials'
fi

echo 'S1H index build runner contract verified'
