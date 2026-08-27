#!/usr/bin/env bash
#
# Ray Training Platform multi-image build and push script.
#
# Default registry is the private Harbor project used by the development flow.
# Override REGISTRY for another namespace or registry. The script never logs
# in for you and never accepts credentials as arguments; use `docker login`
# or an existing CI credential before PUSH_IMAGE=true.
#
# Examples:
#   bash build-image.sh --help
#   DRY_RUN=true bash build-image.sh
#   PUSH_IMAGE=true IMAGE_TAG=test-20260809 bash build-image.sh
#   BUILD_TARGETS=backend,frontend PUSH_IMAGE=true bash build-image.sh
#   BUILD_TARGETS=test-training PUSH_IMAGE=true bash build-image.sh
#
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

REGISTRY="${REGISTRY:-harbor.wellspiking.ai/guofeng.su}"
IMAGE_TAG="${IMAGE_TAG:-test-$(date -u +%Y%m%d-%H%M%S)}"
BUILD_TARGETS_RAW="${BUILD_TARGETS:-all}"
PUSH_IMAGE="${PUSH_IMAGE:-false}"
USE_BUILDX="${USE_BUILDX:-false}"
BUILD_PLATFORM="${BUILD_PLATFORM:-linux/amd64}"
DRY_RUN="${DRY_RUN:-false}"
NO_CACHE="${NO_CACHE:-false}"
PULL_BASE_IMAGES="${PULL_BASE_IMAGES:-false}"

GOPROXY_ARG="${GOPROXY:-https://goproxy.cn,direct}"
NPM_REGISTRY_ARG="${NPM_REGISTRY:-https://registry.npmmirror.com}"
APK_MIRROR_ARG="${APK_MIRROR:-https://repo.huaweicloud.com/repository/alpine}"
GO_BUILDER_IMAGE_ARG="${GO_BUILDER_IMAGE:-swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/golang:1.25-alpine@sha256:a9316ea600fe38d4527999823d67764dbd5b5ce4b4a0895266faf0134aa28264}"
NODE_BUILDER_IMAGE_ARG="${NODE_BUILDER_IMAGE:-swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/node:20-alpine}"
ALPINE_RUNTIME_IMAGE_ARG="${ALPINE_RUNTIME_IMAGE:-swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/alpine:3.20}"
NGINX_RUNTIME_IMAGE_ARG="${NGINX_RUNTIME_IMAGE:-harbor.wellspiking.ai/hub/nginxinc/nginx-unprivileged:1.31-alpine}"
# Batch workloads import CUDA PyTorch and therefore need the Ray GPU runtime.
# The workspace has always used this image; keeping the training default aligned
# prevents a newly built "default" training image from silently lacking CUDA
# runtime libraries on the 4090 worker pool.
RAY_BASE_IMAGE_ARG="${RAY_BASE_IMAGE:-harbor.wellspiking.ai/hub/rayproject/ray:2.35.0-py310-gpu}"
WORKSPACE_RAY_BASE_IMAGE_ARG="${WORKSPACE_RAY_BASE_IMAGE:-harbor.wellspiking.ai/hub/rayproject/ray:2.35.0-py310-gpu}"
RAY_PRODUCTION_VERSION="2.56.1"
RAY_VERSION_ARG="${RAY_VERSION:-$RAY_PRODUCTION_VERSION}"
RAY_RUNTIME_VARIANTS=(pytorch-ray-ddp workspace-ray256)
BEVFUSION_BASE_IMAGE_ARG="${BEVFUSION_BASE_IMAGE:-harbor.wellspiking.ai/guofeng.su/bevfusion@sha256:88e9c5045ced1b4b3dc49ddf1f2e22a8c9702574fd8103afcdff83577784a5ee}"
CODE_SERVER_IMAGE_ARG="${CODE_SERVER_IMAGE:-harbor.wellspiking.ai/hub/codercom/code-server:4.93.1}"
SPK_RAYJOB_VERSION_ARG="${SPK_RAYJOB_VERSION:-$IMAGE_TAG}"
# Bash 3.2 with `set -u` treats expansion of an empty array as unbound inside
# an EXIT trap. The empty sentinel keeps early validation failures clean; the
# cleanup helper skips it and only removes exact mktemp files registered later.
TEMP_DOCKERFILES=("")

cleanup_temp_dockerfiles() {
  local dockerfile
  for dockerfile in "${TEMP_DOCKERFILES[@]}"; do
    if [ -n "$dockerfile" ] && [ -f "$dockerfile" ]; then
      rm -f -- "$dockerfile"
    fi
  done
}

trap cleanup_temp_dockerfiles EXIT

trim() {
  local value="${1:-}"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

is_true() {
  case "${1:-}" in
    1|true|TRUE|yes|YES|on|ON) return 0 ;;
    *) return 1 ;;
  esac
}

usage() {
  cat <<'EOF'
Ray Training Platform image builder

Environment variables:
  REGISTRY=harbor.wellspiking.ai/guofeng.su
  IMAGE_TAG=test-20260809
  BUILD_TARGETS=all|backend,frontend,source-materializer,test-training,workspace,train-pytorch,pytorch-ray-ddp,pytorch-ray-train,workspace-ray256,bevfusion-runtime,tos-prefix-init,spk-rayjob
  PUSH_IMAGE=false|true
  USE_BUILDX=true|false
  BUILD_PLATFORM=linux/amd64
  DRY_RUN=false|true
  NO_CACHE=false|true
  PULL_BASE_IMAGES=false|true
  GOPROXY=https://goproxy.cn,direct
  NPM_REGISTRY=https://registry.npmmirror.com
  APK_MIRROR=https://repo.huaweicloud.com/repository/alpine
  GO_BUILDER_IMAGE=swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/golang:1.25-alpine@sha256:a9316ea600fe38d4527999823d67764dbd5b5ce4b4a0895266faf0134aa28264
  NODE_BUILDER_IMAGE=swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/node:20-alpine
  ALPINE_RUNTIME_IMAGE=swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/alpine:3.20
  NGINX_RUNTIME_IMAGE=harbor.wellspiking.ai/hub/nginxinc/nginx-unprivileged:1.31-alpine
  RAY_BASE_IMAGE=harbor.wellspiking.ai/hub/rayproject/ray:2.35.0-py310-gpu
  WORKSPACE_RAY_BASE_IMAGE=harbor.wellspiking.ai/hub/rayproject/ray:2.35.0-py310-gpu
  RAY_VERSION=2.56.1 (production runtime variants are fixed to this version)
  BEVFUSION_BASE_IMAGE=harbor.wellspiking.ai/guofeng.su/bevfusion@sha256:...
  CODE_SERVER_IMAGE=harbor.wellspiking.ai/hub/codercom/code-server:4.93.1
  SPK_RAYJOB_VERSION=<release-version>

Build targets:
  backend             Go API/control-plane image
  frontend            Vue/Nginx portal image
  source-materializer Git and governed-workspace code materializer image
  test-training       Single-GPU smoke Ray image
  workspace           Existing Ray 2.35 interactive workspace (rollback)
  train-pytorch       Existing Ray 2.35 PyTorch runtime (rollback)
  pytorch-ray-ddp     Ray 2.56.1 PyTorch runtime for Ray-orchestrated DDP
  pytorch-ray-train   Explicit Task 9 target for managed Ray Train
  workspace-ray256    Ray 2.56.1 interactive workspace
  tos-prefix-init     Native TOS SDK utility for controlled training roots
  spk-rayjob          Self-service external submission CLI release image
  all                 All currently buildable platform images (default)

The script requires an authenticated Docker/BuildKit session when pushing.
It prints remote image digests after a successful push.
EOF
}

target_spec() {
  case "$1" in
    backend)
      printf '%s\n' 'backend/Dockerfile|ray-train-backend|backend|-'
      ;;
    frontend)
      printf '%s\n' 'frontend/Dockerfile|ray-train-frontend|frontend|-'
      ;;
    source-materializer)
      printf '%s\n' 'images/source-materializer/Dockerfile|ray-source-materializer|.|-'
      ;;
    test-training)
      printf '%s\n' 'images/test-training/Dockerfile|ray-test|images/test-training|-'
      ;;
    tos-prefix-init)
      printf '%s\n' 'images/tos-prefix-init/Dockerfile|ray-tos-prefix-init|.|-'
      ;;
    workspace)
      printf '%s\n' 'images/workspace/Dockerfile|ray-workspace|images/workspace|workspace-legacy'
      ;;
    train-pytorch)
      # The default training runtime shares the tested Ray placement launcher
      # with the interactive workspace image, so its build context is images.
      printf '%s\n' 'images/train-pytorch/Dockerfile|ray-train-pytorch|images|pytorch-legacy'
      ;;
    pytorch-ray-ddp)
      printf '%s\n' 'images/train-pytorch/Dockerfile|ray-train-pytorch-ray-ddp|images|pytorch-ray-ddp'
      ;;
    pytorch-ray-train)
      printf '%s\n' 'images/train-pytorch/Dockerfile|ray-train-pytorch-ray-train|images|pytorch-ray-train'
      ;;
    workspace-ray256)
      printf '%s\n' 'images/workspace/Dockerfile|ray-workspace-ray256|images/workspace|workspace-ray256'
      ;;
    bevfusion-runtime)
      printf '%s\n' 'images/bevfusion-runtime/Dockerfile|ray-train-bevfusion|.|-'
      ;;
    spk-rayjob)
      printf '%s\n' 'backend/Dockerfile.spk-rayjob|spk-rayjob-release|backend|-'
      ;;
    *)
      return 1
      ;;
  esac
}

normalize_targets() {
  local raw target
  if [ "$(trim "$BUILD_TARGETS_RAW")" = "all" ]; then
    printf '%s\n' backend frontend source-materializer test-training workspace train-pytorch "${RAY_RUNTIME_VARIANTS[@]}" bevfusion-runtime tos-prefix-init spk-rayjob
    return
  fi

  IFS=',' read -r -a requested <<< "$BUILD_TARGETS_RAW"
  for raw in "${requested[@]}"; do
    target="$(trim "$raw")"
    [ -n "$target" ] || continue
    target_spec "$target" >/dev/null || {
      echo "ERROR: unknown BUILD_TARGETS entry: $target" >&2
      echo "       valid values: backend, frontend, source-materializer, test-training, workspace, train-pytorch, pytorch-ray-ddp, pytorch-ray-train, workspace-ray256, bevfusion-runtime, tos-prefix-init, spk-rayjob, all" >&2
      exit 1
    }
    printf '%s\n' "$target"
  done
}

run_or_print() {
  if is_true "$DRY_RUN"; then
    printf '+ '
    printf '%q ' "$@"
    printf '\n'
  else
    "$@"
  fi
}

remote_digest() {
  local image="$1"
  local digest=""
  if command -v docker >/dev/null 2>&1 && docker buildx version >/dev/null 2>&1; then
    digest="$(docker buildx imagetools inspect "$image" 2>/dev/null | awk '$1 == "Digest:" { print $2; exit }' || true)"
  fi
  if [ -n "$digest" ]; then
    printf '%s@%s' "$image" "$digest"
  else
    printf '%s' "$image"
  fi
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi
if [ "$#" -gt 0 ]; then
  echo "ERROR: unknown argument: $1" >&2
  echo "       use --help for configuration through environment variables" >&2
  exit 1
fi

REGISTRY="${REGISTRY%/}"
[ -n "$REGISTRY" ] || { echo 'ERROR: REGISTRY cannot be empty' >&2; exit 1; }
[ -n "$IMAGE_TAG" ] || { echo 'ERROR: IMAGE_TAG cannot be empty' >&2; exit 1; }

BUILD_TARGETS_LIST=()
while IFS= read -r normalized_target; do
  [ -n "$normalized_target" ] && BUILD_TARGETS_LIST+=("$normalized_target")
done < <(normalize_targets)
[ "${#BUILD_TARGETS_LIST[@]}" -gt 0 ] || { echo 'ERROR: BUILD_TARGETS is empty' >&2; exit 1; }

for target in "${BUILD_TARGETS_LIST[@]}"; do
  if [ "$target" = "pytorch-ray-train" ] && [ ! -f "$SCRIPT_DIR/images/workspace/raytrain-managed" ]; then
    echo 'ERROR: pytorch-ray-train requires images/workspace/raytrain-managed from Task 9' >&2
    exit 1
  fi
  case "$target" in
    pytorch-ray-ddp|pytorch-ray-train|workspace-ray256)
      [ "$RAY_VERSION_ARG" = "$RAY_PRODUCTION_VERSION" ] || {
        echo "ERROR: production runtime variants require RAY_VERSION=$RAY_PRODUCTION_VERSION" >&2
        exit 1
      }
      ;;
  esac
done

if ! is_true "$DRY_RUN"; then
  command -v docker >/dev/null 2>&1 || { echo 'ERROR: Docker is not installed' >&2; exit 1; }
  docker info >/dev/null 2>&1 || { echo 'ERROR: Docker daemon is not available' >&2; exit 1; }
  if is_true "$USE_BUILDX"; then
    docker buildx version >/dev/null 2>&1 || { echo 'ERROR: Docker Buildx is required when USE_BUILDX=true' >&2; exit 1; }
  fi
fi

echo '============================================='
echo ' Ray Training Platform image build'
echo '============================================='
echo "Registry:       $REGISTRY"
echo "Image tag:      $IMAGE_TAG"
echo "Platform:       $BUILD_PLATFORM"
echo "Buildx:         $USE_BUILDX"
echo "Push:           $PUSH_IMAGE"
echo "Dry run:        $DRY_RUN"
echo "Go proxy:       $GOPROXY_ARG"
echo "NPM registry:   $NPM_REGISTRY_ARG"
echo "APK mirror:     $APK_MIRROR_ARG"
echo "Go builder:     $GO_BUILDER_IMAGE_ARG"
echo "Node builder:   $NODE_BUILDER_IMAGE_ARG"
echo "Alpine runtime: $ALPINE_RUNTIME_IMAGE_ARG"
echo "Nginx runtime:  $NGINX_RUNTIME_IMAGE_ARG"
echo "Ray base:       $RAY_BASE_IMAGE_ARG"
echo "Workspace base: $WORKSPACE_RAY_BASE_IMAGE_ARG"
echo "Ray production: $RAY_VERSION_ARG"
echo "BEVFusion base: $BEVFUSION_BASE_IMAGE_ARG"
echo "Code server:    $CODE_SERVER_IMAGE_ARG"
echo "Targets:        ${BUILD_TARGETS_LIST[*]}"
echo ''

for target in "${BUILD_TARGETS_LIST[@]}"; do
  IFS='|' read -r dockerfile image_name context_dir docker_target <<< "$(target_spec "$target")"
  dockerfile_path="$SCRIPT_DIR/$dockerfile"
  context_path="$SCRIPT_DIR/$context_dir"
  case "$target" in
    pytorch-ray-ddp|pytorch-ray-train|workspace-ray256)
      image="$REGISTRY/$image_name:$RAY_VERSION_ARG-$IMAGE_TAG"
      ;;
    *)
      image="$REGISTRY/$image_name:$IMAGE_TAG"
      ;;
  esac

  [ -f "$dockerfile_path" ] || { echo "ERROR: missing Dockerfile: $dockerfile_path" >&2; exit 1; }
  [ -d "$context_path" ] || { echo "ERROR: missing build context: $context_path" >&2; exit 1; }

  echo "--- Building $target"
  echo "    Image: $image"

  if ! is_true "$USE_BUILDX"; then
    # The legacy Docker builder supports ARG in FROM only for the first stage.
    # Resolve the base-image variables into a temporary Dockerfile so later
    # stages build correctly without requiring BuildKit/Buildx.
    tmp_dockerfile=$(mktemp)
    TEMP_DOCKERFILES+=("$tmp_dockerfile")
    sed \
      -e "s|^FROM \${GO_BUILDER_IMAGE}|FROM $GO_BUILDER_IMAGE_ARG|" \
      -e "s|^FROM \${NODE_BUILDER_IMAGE}|FROM $NODE_BUILDER_IMAGE_ARG|" \
      -e "s|^FROM \${ALPINE_RUNTIME_IMAGE}|FROM $ALPINE_RUNTIME_IMAGE_ARG|" \
      -e "s|^FROM \${NGINX_RUNTIME_IMAGE}|FROM $NGINX_RUNTIME_IMAGE_ARG|" \
      -e "s|^FROM \${RAY_BASE_IMAGE}|FROM $RAY_BASE_IMAGE_ARG|" \
      -e "s|^FROM \${WORKSPACE_RAY_BASE_IMAGE}|FROM $WORKSPACE_RAY_BASE_IMAGE_ARG|" \
      -e "s|^FROM \${BEVFUSION_BASE_IMAGE}|FROM $BEVFUSION_BASE_IMAGE_ARG|" \
      -e "s|^FROM \${CODE_SERVER_IMAGE}|FROM $CODE_SERVER_IMAGE_ARG|" \
      "$dockerfile_path" >"$tmp_dockerfile"
    dockerfile_path="$tmp_dockerfile"
  fi

  if is_true "$USE_BUILDX"; then
    build_cmd=(docker buildx build
      --platform "$BUILD_PLATFORM"
      --file "$dockerfile_path"
      --tag "$image"
      --build-arg "GOPROXY=$GOPROXY_ARG"
      --build-arg "NPM_REGISTRY=$NPM_REGISTRY_ARG"
      --build-arg "APK_MIRROR=$APK_MIRROR_ARG"
      --build-arg "GO_BUILDER_IMAGE=$GO_BUILDER_IMAGE_ARG"
      --build-arg "NODE_BUILDER_IMAGE=$NODE_BUILDER_IMAGE_ARG"
      --build-arg "ALPINE_RUNTIME_IMAGE=$ALPINE_RUNTIME_IMAGE_ARG"
      --build-arg "NGINX_RUNTIME_IMAGE=$NGINX_RUNTIME_IMAGE_ARG"
      --build-arg "RAY_BASE_IMAGE=$RAY_BASE_IMAGE_ARG"
      --build-arg "WORKSPACE_RAY_BASE_IMAGE=$WORKSPACE_RAY_BASE_IMAGE_ARG"
      --build-arg "RAY_VERSION=${RAY_VERSION_ARG}"
      --build-arg "BEVFUSION_BASE_IMAGE=$BEVFUSION_BASE_IMAGE_ARG"
      --build-arg "CODE_SERVER_IMAGE=$CODE_SERVER_IMAGE_ARG"
      --build-arg "SPK_RAYJOB_VERSION=$SPK_RAYJOB_VERSION_ARG"
    )
    [ "$docker_target" = "-" ] || build_cmd+=(--target "$docker_target")
    is_true "$NO_CACHE" && build_cmd+=(--no-cache)
    is_true "$PULL_BASE_IMAGES" && build_cmd+=(--pull)
    if is_true "$PUSH_IMAGE"; then
      build_cmd+=(--push)
    else
      build_cmd+=(--load)
    fi
    build_cmd+=("$context_path")
  else
    # The legacy Docker builder on the build host already produces the host
    # platform (linux/amd64 here). Passing --platform to Docker 29's image
    # store can retain an incomplete manifest index and make Harbor pushes
    # fail with missing foreign-platform blobs. Cross-platform builds require
    # buildx; the non-buildx path intentionally stays native and single-arch.
    build_cmd=(docker build
      --file "$dockerfile_path"
      --tag "$image"
      --build-arg "GOPROXY=$GOPROXY_ARG"
      --build-arg "NPM_REGISTRY=$NPM_REGISTRY_ARG"
      --build-arg "APK_MIRROR=$APK_MIRROR_ARG"
      --build-arg "GO_BUILDER_IMAGE=$GO_BUILDER_IMAGE_ARG"
      --build-arg "NODE_BUILDER_IMAGE=$NODE_BUILDER_IMAGE_ARG"
      --build-arg "ALPINE_RUNTIME_IMAGE=$ALPINE_RUNTIME_IMAGE_ARG"
      --build-arg "NGINX_RUNTIME_IMAGE=$NGINX_RUNTIME_IMAGE_ARG"
      --build-arg "RAY_BASE_IMAGE=$RAY_BASE_IMAGE_ARG"
      --build-arg "WORKSPACE_RAY_BASE_IMAGE=$WORKSPACE_RAY_BASE_IMAGE_ARG"
      --build-arg "RAY_VERSION=${RAY_VERSION_ARG}"
      --build-arg "BEVFUSION_BASE_IMAGE=$BEVFUSION_BASE_IMAGE_ARG"
      --build-arg "CODE_SERVER_IMAGE=$CODE_SERVER_IMAGE_ARG"
      --build-arg "SPK_RAYJOB_VERSION=$SPK_RAYJOB_VERSION_ARG"
    )
    [ "$docker_target" = "-" ] || build_cmd+=(--target "$docker_target")
    is_true "$NO_CACHE" && build_cmd+=(--no-cache)
    is_true "$PULL_BASE_IMAGES" && build_cmd+=(--pull)
    build_cmd+=("$context_path")
  fi

  run_or_print "${build_cmd[@]}"

  if is_true "$PUSH_IMAGE" && ! is_true "$USE_BUILDX"; then
    # The legacy path above always emits a native single-platform image, so a
    # normal push is both portable and sufficient for the amd64 VKE workers.
    if is_true "$DRY_RUN"; then
      echo "+ docker push $image"
    else
      docker push "$image"
    fi
  fi

  if is_true "$PUSH_IMAGE" && ! is_true "$DRY_RUN"; then
    echo "    Digest: $(remote_digest "$image")"
  fi

  echo "    Done:   $image"
done

echo ''
echo 'Build complete.'
if is_true "$PUSH_IMAGE"; then
  echo 'Use the printed @sha256 digests for backend images and rayTrain.runtimeCatalog; never replace the Ray 2.35 rollback entries.'
else
  echo 'Images were not pushed. Set PUSH_IMAGE=true after docker login to push them.'
fi
