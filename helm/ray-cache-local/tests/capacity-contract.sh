#!/usr/bin/env bash
set -euo pipefail

readonly chart_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly setup_script="${chart_dir}/files/setup"
readonly teardown_script="${chart_dir}/files/teardown"

[[ -f "${setup_script}" ]] || { echo 'missing files/setup' >&2; exit 1; }
[[ -f "${teardown_script}" ]] || { echo 'missing files/teardown' >&2; exit 1; }

test_dir="$(mktemp -d)"
test_dir="$(cd -P "${test_dir}" && pwd -P)"
trap 'rm -rf "${test_dir}"' EXIT
root1="${test_dir}/data1/ray-cache"
root2="${test_dir}/data2/ray-cache"
unknown_root="${test_dir}/unknown/ray-cache"
mkdir -p "${root1}" "${root2}" "${unknown_root}"

healthy_df="${test_dir}/df-healthy"
low_df="${test_dir}/df-low"
cat >"${healthy_df}" <<'EOF'
Filesystem 1024-blocks Used Available Capacity Mounted on
/dev/test 100000 20000 80000 20% /test
EOF
cat >"${low_df}" <<'EOF'
Filesystem 1024-blocks Used Available Capacity Mounted on
/dev/test 100000 80000 20000 80% /test
EOF

readonly test_uid="$(id -u)"
readonly test_gid="$(id -g)"
readonly allowed_roots="${root1}:${root2}"

run_setup() {
  local target="$1"
  local size="$2"
  local df_file="${3:-${healthy_df}}"
  env \
    RAY_CACHE_TEST_MODE=1 \
    RAY_CACHE_TEST_ALLOWED_ROOTS="${allowed_roots}" \
    RAY_CACHE_TEST_DF_FILE="${df_file}" \
    RAY_CACHE_TEST_UID="${test_uid}" \
    RAY_CACHE_TEST_GID="${test_gid}" \
    VOL_DIR="${target}" \
    VOL_SIZE_BYTES="${size}" \
    "${setup_script}"
}

run_teardown() {
  local target="$1"
  shift
  env \
    RAY_CACHE_TEST_MODE=1 \
    RAY_CACHE_TEST_ALLOWED_ROOTS="${allowed_roots}" \
    "$@" \
    VOL_DIR="${target}" \
    "${teardown_script}"
}

expect_failure() {
  if "$@" >/dev/null 2>&1; then
    echo "expected failure: $*" >&2
    exit 1
  fi
}

volume1="${root1}/pvc-11111111-1111-4111-8111-111111111111_team-a_cache-a"
run_setup "${volume1}" 10485760
[[ -d "${volume1}" ]] || { echo 'normal setup did not create target' >&2; exit 1; }
if mode="$(stat -c '%a' "${volume1}" 2>/dev/null)" && [[ "${mode}" =~ ^[0-7]{3,4}$ ]]; then
  :
else
  mode="$(stat -f '%Lp' "${volume1}")"
fi
[[ "${mode}" == 770 ]] || {
  echo 'setup target mode is not 0770' >&2
  exit 1
}

low_volume="${root1}/pvc-22222222-2222-4222-8222-222222222222_team-a_cache-b"
expect_failure run_setup "${low_volume}" 10485760 "${low_df}"
[[ ! -e "${low_volume}" ]] || { echo 'low-capacity setup created a target' >&2; exit 1; }

unknown_volume="${unknown_root}/pvc-33333333-3333-4333-8333-333333333333_team-a_cache-c"
expect_failure run_setup "${unknown_volume}" 10485760
expect_failure run_setup "${root1}/pvc-44444444-4444-4444-8444-444444444444_team-a_cache-d" 0
expect_failure run_setup "${root1}/pvc-55555555-5555-4555-8555-555555555555_team-a_cache-e" invalid

# A retry for the exact existing target is idempotent even after free space drops.
run_setup "${volume1}" 10485760 "${low_df}"

keep_volume="${root1}/pvc-66666666-6666-4666-8666-666666666666_team-a_cache-f"
run_setup "${keep_volume}" 10485760
printf 'cache-data\n' >"${volume1}/payload"
run_teardown "${volume1}"
[[ ! -e "${volume1}" ]] || { echo 'teardown did not remove exact target' >&2; exit 1; }
[[ -d "${keep_volume}" ]] || { echo 'teardown removed a sibling target' >&2; exit 1; }

expect_failure run_teardown "${root1}"
expect_failure run_teardown "$(dirname -- "${root1}")"
expect_failure run_teardown "${test_dir}"

failure_volume="${root2}/pvc-77777777-7777-4777-8777-777777777777_team-a_cache-g"
run_setup "${failure_volume}" 10485760
expect_failure run_teardown "${failure_volume}" RAY_CACHE_TEST_FORCE_DELETE_FAILURE=1
[[ "$(cat "${root2}/.ray-cache-metrics/teardown_failures")" == 1 ]] || {
  echo 'teardown failure metric was not incremented' >&2
  exit 1
}

echo 'ray cache capacity and teardown contract verified'
