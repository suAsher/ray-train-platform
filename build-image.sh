#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
if [[ ${1:-} == --help ]]; then
  echo 'REGISTRY=<registry/project> IMAGE_TAG=dev BUILD_TARGETS=platform,source-materializer,workspace,train-pytorch PUSH_IMAGE=false bash build-image.sh'
  exit 0
fi
: "${REGISTRY:?Set REGISTRY to your own registry/project}"
IMAGE_TAG=${IMAGE_TAG:-dev}
BUILD_TARGETS=${BUILD_TARGETS:-platform}
PUSH_IMAGE=${PUSH_IMAGE:-false}
IFS=',' read -r -a targets <<< "$BUILD_TARGETS"
for target in "${targets[@]}"; do
  context=.
  case "$target" in
    platform) file=Dockerfile; image=ray-train-platform ;;
    source-materializer) file=images/source-materializer/Dockerfile; image=ray-source-materializer ;;
    workspace) file=images/workspace/Dockerfile; image=ray-workspace; context=images/workspace ;;
    train-pytorch) file=images/train-pytorch/Dockerfile; image=ray-train-pytorch; context=images ;;
    *) echo "Unknown target: $target" >&2; exit 2 ;;
  esac
  ref="$REGISTRY/$image:$IMAGE_TAG"
  docker build -f "$file" -t "$ref" "$context"
  if [[ $PUSH_IMAGE == true ]]; then docker push "$ref"; fi
 done
