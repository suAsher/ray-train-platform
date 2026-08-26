#!/usr/bin/env bash
set -euo pipefail

bundle_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
image='harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553'

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_contains() {
  local file="$1"
  local expected="$2"
  grep -F -- "$expected" "$file" >/dev/null || fail "$(basename "$file") missing: $expected"
}

for script in \
  01-single-8gpu-no-cache.sh \
  02-multi-16gpu-no-cache.sh \
  03-single-8gpu-dual-nvme.sh \
  04-multi-16gpu-dual-nvme.sh; do
  path="$bundle_dir/$script"
  [[ -f "$path" ]] || fail "$script does not exist"
  [[ -x "$path" ]] || fail "$script is not executable"
  bash -n "$path"
  assert_contains "$path" "$image"
  assert_contains "$path" '/root/raytrain-benchmark-auth.json'
  assert_contains "$path" '/opt/guofeng/welldrive-train/raytrain-perf-bevfusion'
  assert_contains "$path" 'training_benchmark.py'
  assert_contains "$path" '--gpus-per-worker 8'
  assert_contains "$path" '--cpu-per-worker 128'
  assert_contains "$path" '--memory-per-worker 512Gi'
  assert_contains "$path" '--input-space public'
  assert_contains "$path" '--input-path'
  assert_contains "$path" '0429_pkl/fz/merged_nuscenes_infos_train.pkl'
  assert_contains "$path" '0429_pkl/fz/merged_nuscenes_infos_val.pkl'
  assert_contains "$path" 'date +%Y%m%d-%H%M%S'
  if grep -F -- '/tmp/' "$path" >/dev/null; then
    fail "$script must not depend on /tmp"
  fi
  if grep -E -- '(kubeconfig|kubectl)' "$path" >/dev/null; then
    fail "$script must not require Kubernetes privileges"
  fi
done

for script in 01-single-8gpu-no-cache.sh 02-multi-16gpu-no-cache.sh; do
  assert_contains "$bundle_dir/$script" '--cache-mode off'
done

for script in 03-single-8gpu-dual-nvme.sh 04-multi-16gpu-dual-nvme.sh; do
  assert_contains "$bundle_dir/$script" '--cache-mode runtime'
  assert_contains "$bundle_dir/$script" '--cache-size 5Ti'
  assert_contains "$bundle_dir/$script" '--stage-cache'
done

assert_contains "$bundle_dir/01-single-8gpu-no-cache.sh" '--workers 1'
assert_contains "$bundle_dir/01-single-8gpu-no-cache.sh" '--execution-mode torchrun'
assert_contains "$bundle_dir/03-single-8gpu-dual-nvme.sh" '--workers 1'
assert_contains "$bundle_dir/03-single-8gpu-dual-nvme.sh" '--execution-mode torchrun'
assert_contains "$bundle_dir/02-multi-16gpu-no-cache.sh" '--workers 2'
assert_contains "$bundle_dir/02-multi-16gpu-no-cache.sh" '--execution-mode ray_train'
assert_contains "$bundle_dir/04-multi-16gpu-dual-nvme.sh" '--workers 2'
assert_contains "$bundle_dir/04-multi-16gpu-dual-nvme.sh" '--execution-mode ray_train'

PYTHONPYCACHEPREFIX=/tmp/raytrain-benchmark-pycache \
  python3 -m py_compile "$bundle_dir/training_benchmark.py"
echo "PASS: raytrain BEVFusion benchmark bundle contract"
