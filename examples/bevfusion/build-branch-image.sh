#!/usr/bin/env bash
set -euo pipefail

# Build a pinned BEVFusion branch image without altering the checked-out
# source. Usage:
#   examples/bevfusion/build-branch-image.sh /path/to/bevfusion bev-3dod-<commit>
# Set BEVFUSION_EXTENSION_MODE=prebuilt on a CPU-only builder.  It retains
# the extension ABI from BASE_IMAGE and is suitable only for migration and
# preflight.  The default rebuild mode requires a CUDA-capable image builder.
# Set PUSH_IMAGE=false to only build locally.  Docker 29-compatible OCI
# pushing (including the lazy-content-store fallback) is handled by the helper.
source_dir="${1:?source repository is required}"
tag="${2:?target tag is required}"

if [[ ! -d "$source_dir/.git" ]]; then
  echo "source repository must be a Git checkout: $source_dir" >&2
  exit 2
fi

platform_root="$(cd "$(dirname "$0")/../.." && pwd)"
context_dir="$(mktemp -d)"
cleanup() { rm -rf "$context_dir"; }
trap cleanup EXIT

git -C "$source_dir" archive --format=tar HEAD | tar -x -C "$context_dir"
cp "$platform_root/examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh" "$context_dir/"

base_image="${BASE_IMAGE:-harbor.wellspiking.ai/model_evalution/bevfusion@sha256:b0006e8b1cadf68a6ce02710ff8a63a900c155a63b4b4aa6f827a5c3e2a3a08b}"
target_image="${REGISTRY:-harbor.wellspiking.ai/guofeng.su}/bevfusion:$tag"
extension_mode="${BEVFUSION_EXTENSION_MODE:-rebuild}"

case "$extension_mode" in
  rebuild) dockerfile="$platform_root/examples/bevfusion/Dockerfile" ;;
  prebuilt) dockerfile="$platform_root/examples/bevfusion/Dockerfile.source-overlay" ;;
  *)
    echo "BEVFUSION_EXTENSION_MODE must be rebuild or prebuilt" >&2
    exit 2
    ;;
esac

docker build --pull=false \
  --build-arg "BASE_IMAGE=$base_image" \
  -f "$dockerfile" \
  -t "$target_image" "$context_dir"

if [[ "${PUSH_IMAGE:-true}" == "true" ]]; then
  bash "$platform_root/examples/bevfusion/push-oci-image.sh" "$target_image"
else
  docker image inspect "$target_image" --format 'built locally: {{.Id}}'
fi
