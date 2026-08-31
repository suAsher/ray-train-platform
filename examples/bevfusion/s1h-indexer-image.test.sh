#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
dockerfile="$root_dir/examples/bevfusion/Dockerfile.s1h-indexer"
builder="$root_dir/examples/bevfusion/build-s1h-indexer-image.sh"

fail() {
  printf 's1h indexer image contract: %s\n' "$1" >&2
  exit 1
}

[[ -f "$dockerfile" ]] || fail 'missing Dockerfile.s1h-indexer'
[[ -x "$builder" ]] || fail 'build-s1h-indexer-image.sh must be executable'

grep -Fq 'ARG BASE_IMAGE' "$dockerfile" || fail 'base image must be supplied explicitly'
grep -Fq 'COPY nuscenes_converter.py' "$dockerfile" || fail 'pinned patched converter is missing'
grep -Fq 'COPY generate_s1h_public_indexes.py' "$dockerfile" || fail 'fresh PKL generator is missing'
grep -Fq 'COPY build_s1h_trusted_index.py' "$dockerfile" || fail 'trusted index adapter is missing'
grep -Fq 'COPY raytrain_publisher' "$dockerfile" || fail 'restricted publisher schema package is missing'
grep -Fq 'USER 1000:1000' "$dockerfile" || fail 'runtime must be non-root'

grep -Fq 'git -C "$source_dir" archive' "$builder" || fail 'build input must come from a Git commit'
grep -Fq 's1h_lidar_converter_patch.py' "$builder" || fail 'build must apply the reviewed LiDAR-only patch'
grep -A1 -F 's1h_lidar_converter_patch.py' "$builder" | grep -Fq '"$context_dir"' ||
  fail 'LiDAR-only patch must receive the temporary repository root'
grep -Fq 'docker build --pull=false' "$builder" || fail 'build must not silently change the base image'
grep -Fq 'push-oci-image.sh' "$builder" || fail 'build must use the registry compatibility push helper'

if grep -Eqi '(AKLT|SecretAccessKey|glpat-|rpt_)' "$dockerfile" "$builder"; then
  fail 'image sources must not contain credentials'
fi

echo 'S1H indexer image contract verified'
