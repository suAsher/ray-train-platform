#!/usr/bin/env bash
set -euo pipefail

source_dir="${1:?BEVFusion Git checkout is required}"
tag="${2:?target image tag is required}"

if [[ ! -d "$source_dir/.git" ]]; then
  echo "source directory must be a Git checkout: $source_dir" >&2
  exit 2
fi

platform_root="$(cd "$(dirname "$0")/../.." && pwd)"
context_dir="$(mktemp -d)"
cleanup() { rm -rf "$context_dir"; }
trap cleanup EXIT

mkdir -p "$context_dir/raytrain_publisher"
git -C "$source_dir" archive HEAD mmdet3d tools/data_converter/nuscenes_converter.py |
  tar -x -C "$context_dir"

python3 "$platform_root/examples/bevfusion/patches/s1h_lidar_converter_patch.py" \
  "$context_dir"
mv "$context_dir/tools/data_converter/nuscenes_converter.py" "$context_dir/nuscenes_converter.py"
rm -rf "$context_dir/tools"
cp "$platform_root/examples/bevfusion/scripts/generate_s1h_public_indexes.py" "$context_dir/"
cp "$platform_root/examples/bevfusion/scripts/build_s1h_trusted_index.py" "$context_dir/"
cp "$platform_root/images/dataset-publisher/raytrain_publisher/"*.py "$context_dir/raytrain_publisher/"

base_image="${BASE_IMAGE:-harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion-packed@sha256:dba620167db021356abc8057104800a093589a9ffaa81644f0ffe108bd3a538d}"
target_image="${REGISTRY:-harbor.wellspiking.ai/guofeng.su}/ray-train-s1h-indexer:$tag"

docker build --pull=false \
  --build-arg "BASE_IMAGE=$base_image" \
  -f "$platform_root/examples/bevfusion/Dockerfile.s1h-indexer" \
  -t "$target_image" "$context_dir"

if [[ "${PUSH_IMAGE:-true}" == "true" ]]; then
  bash "$platform_root/examples/bevfusion/push-oci-image.sh" "$target_image"
else
  docker image inspect "$target_image" --format 'built locally: {{.Id}}'
fi

printf '%s\n' "$target_image"
