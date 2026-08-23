#!/usr/bin/env bash
set -euo pipefail

readonly ops_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly preflight_script="${ops_dir}/preflight.sh"

grep -Fq -- 'validate_trusted_path_state()' "${preflight_script}" || {
  echo 'preflight.sh missing trusted path owner/mode validation' >&2
  exit 1
}
if grep -Fq -- 'test -d --' "${preflight_script}"; then
  echo 'preflight.sh uses a non-POSIX test -- operand on remote nodes' >&2
  exit 1
fi

RAY_CACHE_PREFLIGHT_LIBRARY_ONLY=1
export RAY_CACHE_PREFLIGHT_LIBRARY_ONLY
# shellcheck source=/dev/null
source "${preflight_script}"

validate_trusted_path_state 'node:/' '0:0:755' 755
validate_trusted_path_state 'node:/data1' '0:0:755' 755
validate_trusted_path_state 'node:/data1/ray-cache' '0:0:770' 770

for bad_state in '1000:0:770' '0:1000:770' '0:0:775' '0:0:0770' 'invalid'; do
  if validate_trusted_path_state 'node:/data1/ray-cache' "${bad_state}" 770 >/dev/null 2>&1; then
    echo "owner/mode mismatch accepted: ${bad_state}" >&2
    exit 1
  fi
done

RAY_CACHE_EXPECTED_ROOT_GID=1234 \
RAY_CACHE_PREFLIGHT_LIBRARY_ONLY=1 \
PREFLIGHT_SCRIPT="${preflight_script}" \
bash -ceu 'source "${PREFLIGHT_SCRIPT}"; validate_trusted_path_state node:/data1/ray-cache 0:1234:770 770'

echo 'preflight owner and mode contract verified'
