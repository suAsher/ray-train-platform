#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly production_ray_version="2.56.1"

require_literal() {
  local file="$1"
  local literal="$2"
  if ! grep -Fq -- "$literal" "${root_dir}/${file}"; then
    echo "missing runtime image contract in ${file}: ${literal}" >&2
    exit 1
  fi
}

docker_stage() {
  local file="$1"
  local stage="$2"
  awk -v stage="$stage" '
    /^FROM[[:space:]]/ {
      active = ($0 ~ ("[[:space:]]AS[[:space:]]" stage "([[:space:]]|$)"))
    }
    active { print }
  ' "${root_dir}/${file}"
}

assert_contracts() {
  require_literal images/train-pytorch/Dockerfile 'AS pytorch-legacy'
  require_literal images/train-pytorch/Dockerfile 'AS pytorch-ray-ddp'
  require_literal images/train-pytorch/Dockerfile 'AS pytorch-ray-train'
  require_literal images/train-pytorch/Dockerfile 'ARG RAY_VERSION=2.56.1'
  require_literal images/train-pytorch/Dockerfile 'ray[train,tune]==${RAY_VERSION}'
  require_literal images/train-pytorch/Dockerfile 'python3 -c'
  require_literal images/train-pytorch/Dockerfile 'python3 -m pip check'
  require_literal images/train-pytorch/Dockerfile 'COPY workspace/raytrain-managed /usr/local/bin/raytrain-managed'
  require_literal images/train-pytorch/Dockerfile 'ENV RAY_TRAIN_V2_ENABLED=1'

  local ddp_stage managed_stage
  ddp_stage="$(docker_stage images/train-pytorch/Dockerfile pytorch-ray-ddp)"
  managed_stage="$(docker_stage images/train-pytorch/Dockerfile pytorch-ray-train)"
  if grep -Fq 'RAY_TRAIN_V2_ENABLED=1' <<<"$ddp_stage"; then
    echo 'Ray DDP image stage must not enable Ray Train V2' >&2
    exit 1
  fi
  if ! grep -Fq 'ENV RAY_TRAIN_V2_ENABLED=1' <<<"$managed_stage"; then
    echo 'managed Ray Train image stage must explicitly enable Ray Train V2' >&2
    exit 1
  fi

  require_literal images/workspace/Dockerfile 'AS workspace-legacy'
  require_literal images/workspace/Dockerfile 'AS workspace-ray256'
  require_literal images/workspace/Dockerfile 'ARG RAY_VERSION=2.56.1'
  require_literal images/workspace/Dockerfile 'ray[train,tune]==${RAY_VERSION}'
  require_literal images/workspace/Dockerfile 'COPY raytrain-managed /usr/local/bin/raytrain-managed'

  require_literal build-image.sh 'RAY_RUNTIME_VARIANTS'
  require_literal build-image.sh 'pytorch-ray-ddp'
  require_literal build-image.sh 'pytorch-ray-train'
  require_literal build-image.sh 'workspace-ray256'
  require_literal build-image.sh 'RAY_VERSION=${RAY_VERSION_ARG}'
  require_literal build-image.sh "RAY_PRODUCTION_VERSION=\"${production_ray_version}\""

  # The old public build targets and image names are rollback contracts. New
  # variants must be additive; the builder may not retag or remove them.
  require_literal build-image.sh "'images/workspace/Dockerfile|ray-workspace|images/workspace|workspace-legacy'"
  require_literal build-image.sh "'images/train-pytorch/Dockerfile|ray-train-pytorch|images|pytorch-legacy'"
  if grep -Eq 'docker[[:space:]]+(tag|rmi).*(ray-workspace|ray-train-pytorch)' "${root_dir}/build-image.sh"; then
    echo 'runtime builder must not rewrite or delete Ray 2.35 image tags' >&2
    exit 1
  fi

  require_literal helm/ray-train-platform/values.yaml 'managedEnabled: false'
  require_literal helm/ray-train-platform/values.yaml 'canaryEnabled: false'
  require_literal helm/ray-train-platform/values.yaml 'productionVersion: "2.56.1"'
  require_literal helm/ray-train-platform/values.yaml 'supportedEngines: ["ray-ddp"]'
  require_literal helm/ray-train-platform/values.yaml 'supportedEngines: ["ray-train"]'

  local dry_run
  dry_run="$(
    DRY_RUN=true \
    IMAGE_TAG=contract \
    BUILD_TARGETS=workspace,train-pytorch,pytorch-ray-ddp,pytorch-ray-train,workspace-ray256 \
    bash "${root_dir}/build-image.sh"
  )"
  grep -Fq 'ray-workspace:contract' <<<"$dry_run"
  grep -Fq 'ray-train-pytorch:contract' <<<"$dry_run"
  grep -Fq 'ray-train-pytorch-ray-ddp:2.56.1-contract' <<<"$dry_run"
  grep -Fq 'ray-train-pytorch-ray-train:2.56.1-contract' <<<"$dry_run"
  grep -Fq 'ray-workspace-ray256:2.56.1-contract' <<<"$dry_run"
}

require_exact_image_ref() {
  local variable_name="$1"
  local value="$2"
  if [[ -z "$value" ]]; then
    echo "${variable_name} must be set to an immutable image reference" >&2
    exit 1
  fi
  if [[ ! "$value" =~ @sha256:[0-9a-f]{64}$ ]]; then
    echo "${variable_name} must end with @sha256:<64 lowercase hex characters>" >&2
    exit 1
  fi
}

smoke_image() {
  local image="$1"
  local expected_managed="$2"

  docker run --rm "$image" python3 -c \
    "import ray, ray.train, torch; assert ray.__version__ == '${production_ray_version}', ray.__version__; print(ray.__version__, torch.__version__)"
  docker run --rm "$image" sh -eu -c \
    "ray --version | grep -F '${production_ray_version}'; test -x /usr/local/bin/raytrain-launch; test -x /usr/local/bin/raytrain-managed"

  if [[ "$expected_managed" == true ]]; then
    docker run --rm "$image" sh -eu -c 'test "${RAY_TRAIN_V2_ENABLED:-}" = 1'
  else
    docker run --rm "$image" sh -eu -c 'test "${RAY_TRAIN_V2_ENABLED:-}" != 1'
  fi
}

assert_contracts

if [[ "${1:-}" == --contract-only ]]; then
  echo 'Ray runtime image contracts verified'
  exit 0
fi
if [[ $# -gt 0 ]]; then
  echo "unknown argument: $1" >&2
  exit 1
fi

require_exact_image_ref RAY_DDP_IMAGE "${RAY_DDP_IMAGE:-}"
require_exact_image_ref RAY_TRAIN_IMAGE "${RAY_TRAIN_IMAGE:-}"
require_exact_image_ref RAY_WORKSPACE_IMAGE "${RAY_WORKSPACE_IMAGE:-}"
command -v docker >/dev/null 2>&1 || { echo 'docker is required for runtime smoke tests' >&2; exit 1; }

smoke_image "$RAY_DDP_IMAGE" false
smoke_image "$RAY_TRAIN_IMAGE" true
smoke_image "$RAY_WORKSPACE_IMAGE" false

echo 'Ray runtime image smoke tests passed'
