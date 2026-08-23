#!/usr/bin/env bash
set -euo pipefail

readonly ops_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly verify_script="${ops_dir}/verify.sh"

grep -Fq -- 'validate_local_path()' "${verify_script}" || {
  echo 'verify.sh missing validate_local_path' >&2
  exit 1
}
grep -Fq -- 'remote_path_check=' "${verify_script}" || {
  echo 'verify.sh missing fixed remote positional path check' >&2
  exit 1
}
if grep -Fq -- 'test ! -e "${local_path}"' "${verify_script}"; then
  echo 'verify.sh interpolates local_path into remote command' >&2
  exit 1
fi

RAY_CACHE_VERIFY_LIBRARY_ONLY=1
export RAY_CACHE_VERIFY_LIBRARY_ONLY
# shellcheck source=/dev/null
source "${verify_script}"

valid_paths=(
  '/data1/ray-cache/pvc-11111111-1111-4111-8111-111111111111_team-a_cache-a'
  '/data2/ray-cache/pvc-22222222-2222-4222-8222-222222222222_team-b_cache.b'
)
for path in "${valid_paths[@]}"; do
  validate_local_path "${path}" || { echo "valid path rejected: ${path}" >&2; exit 1; }
done

invalid_paths=(
  '/data1/ray-cache'
  '/data1/ray-cache/'
  '/data1/ray-cache/subdir/pvc-11111111-1111-4111-8111-111111111111_team-a_cache-a'
  '/data1/ray-cache/../pvc-11111111-1111-4111-8111-111111111111_team-a_cache-a'
  '/data1/ray-cache/pvc-11111111-1111-4111-8111-111111111111_team-a_cache-a;touch'
  '/data1/ray-cache/pvc-11111111-1111-4111-8111-111111111111_team-a_cache a'
  $'/data1/ray-cache/pvc-11111111-1111-4111-8111-111111111111_team-a_cache-a\necho'
  '/data1/ray-cache/pvc-11111111-1111-4111-8111-111111111111_team-a_$(touch)'
  '/data1/ray-cache/pvc-11111111-1111-4111-8111-111111111111_team-a_`touch`'
  '/data1/ray-cache/pvc-not-a-uuid_team-a_cache-a'
  '/data3/ray-cache/pvc-11111111-1111-4111-8111-111111111111_team-a_cache-a'
)
for path in "${invalid_paths[@]}"; do
  if validate_local_path "${path}" >/dev/null 2>&1; then
    echo "unsafe path accepted: ${path}" >&2
    exit 1
  fi
done

capture_file="$(mktemp)"
trap 'rm -f -- "${capture_file}"' EXIT
ssh_options=(-o BatchMode=yes)
ssh() {
  printf '%s\n' "$@" >"${capture_file}"
}
remote_path_absent 172.28.1.232 "${valid_paths[0]}"
expected_remote_args="$(printf '%s\n' \
  -o \
  BatchMode=yes \
  root@172.28.1.232 \
  'sh -ceu '\''test ! -e -- "$1"'\'' sh' \
  "${valid_paths[0]}")"
[[ "$(cat "${capture_file}")" == "${expected_remote_args}" ]] || {
  echo 'verify.sh did not pass the validated path as a separate remote positional argument' >&2
  exit 1
}
if remote_path_absent 172.28.1.232 "${invalid_paths[4]}" >/dev/null 2>&1; then
  echo 'verify.sh attempted a remote check for an unsafe path' >&2
  exit 1
fi

echo 'verify local path validation contract verified'
