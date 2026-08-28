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

require_absent_literal() {
  local file="$1"
  local literal="$2"
  if grep -Fq -- "$literal" "${root_dir}/${file}"; then
    echo "forbidden runtime image contract in ${file}: ${literal}" >&2
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

require_before() {
  local text="$1"
  local earlier="$2"
  local later="$3"
  local earlier_line later_line
  earlier_line="$(grep -nF -- "$earlier" <<<"$text" | head -1 | cut -d: -f1)"
  later_line="$(grep -nF -- "$later" <<<"$text" | head -1 | cut -d: -f1)"
  if [[ -z "$earlier_line" || -z "$later_line" || "$earlier_line" -ge "$later_line" ]]; then
    echo "runtime image contract requires '${earlier}' before '${later}'" >&2
    exit 1
  fi
}

assert_contracts() {
  require_literal images/train-pytorch/Dockerfile 'AS pytorch-legacy'
  require_literal images/train-pytorch/Dockerfile 'AS pytorch-ray-ddp'
  require_literal images/train-pytorch/Dockerfile 'AS pytorch-ray-train'
  require_literal images/train-pytorch/Dockerfile 'ARG RAY_VERSION=2.56.1'
  require_literal images/train-pytorch/Dockerfile 'test "${RAY_VERSION}" = "2.56.1"'
  require_literal images/train-pytorch/Dockerfile 'ray[default,train,tune]==${RAY_VERSION}'
  require_literal images/train-pytorch/Dockerfile 'python3 -c'
  require_literal images/train-pytorch/Dockerfile 'python3 -m pip check'
  require_literal images/train-pytorch/Dockerfile 'COPY workspace/raytrain-managed /usr/local/bin/raytrain-managed'
  require_literal images/train-pytorch/Dockerfile 'COPY workspace/raytrain_runtime /usr/local/lib/raytrain-platform/raytrain_runtime'
  require_literal images/train-pytorch/Dockerfile 'ENV RAY_TRAIN_V2_ENABLED=1'

  local base_stage ddp_stage managed_stage managed_env_count required_copy_source
  base_stage="$(docker_stage images/train-pytorch/Dockerfile pytorch-ray256-base)"
  ddp_stage="$(docker_stage images/train-pytorch/Dockerfile pytorch-ray-ddp)"
  managed_stage="$(docker_stage images/train-pytorch/Dockerfile pytorch-ray-train)"
  managed_env_count="$(grep -Fc 'RAY_TRAIN_V2_ENABLED=1' "${root_dir}/images/train-pytorch/Dockerfile")"
  if [[ "$managed_env_count" != 1 ]]; then
    echo "RAY_TRAIN_V2_ENABLED=1 must occur exactly once, found ${managed_env_count}" >&2
    exit 1
  fi
  for non_managed_stage in "$base_stage" "$ddp_stage"; do
    if grep -Fq 'RAY_TRAIN_V2_ENABLED=1' <<<"$non_managed_stage"; then
      echo 'Ray Train V2 must not be enabled in a shared or DDP stage' >&2
      exit 1
    fi
    if grep -Fq 'raytrain-managed' <<<"$non_managed_stage"; then
      echo 'shared and DDP stages must not depend on the Task 9 managed launcher' >&2
      exit 1
    fi
  done
  if ! grep -Fq 'ENV RAY_TRAIN_V2_ENABLED=1' <<<"$managed_stage"; then
    echo 'managed Ray Train image stage must explicitly enable Ray Train V2' >&2
    exit 1
  fi
  if ! grep -Fq 'COPY workspace/raytrain-managed /usr/local/bin/raytrain-managed' <<<"$managed_stage"; then
    echo 'only the managed stage may copy the Task 9 launcher' >&2
    exit 1
  fi
  require_before "$base_stage" 'test "${RAY_VERSION}" = "2.56.1"' 'python3 -m pip install'
  for dependency_contract in \
    'python3 -m pip uninstall -y' \
    'azure-cli-core' \
    'conda-content-trust' \
    '      opentelemetry-exporter-otlp \' \
    'opentelemetry-exporter-otlp-proto-grpc' \
    '"google-api-core==2.34.0"' \
    '"googleapis-common-protos==1.75.2"' \
    '"opentelemetry-exporter-otlp==1.44.0"' \
    'import opentelemetry.exporter.prometheus'; do
    if ! grep -Fq -- "$dependency_contract" <<<"$base_stage"; then
      echo "Ray 2.56 training stage is missing dependency normalization: ${dependency_contract}" >&2
      exit 1
    fi
  done
  require_before "$base_stage" 'python3 -m pip uninstall -y' 'python3 -m pip install'
  require_before "$base_stage" 'python3 -m pip install' 'python3 -m pip check'

  require_literal images/workspace/Dockerfile 'AS workspace-legacy'
  require_literal images/workspace/Dockerfile 'AS workspace-ray256'
  require_literal images/workspace/Dockerfile 'ARG RAY_VERSION=2.56.1'
  require_literal images/workspace/Dockerfile 'test "${RAY_VERSION}" = "2.56.1"'
  require_literal images/workspace/Dockerfile 'ray[default,train,tune]==${RAY_VERSION}'
  require_absent_literal images/workspace/Dockerfile 'raytrain-managed'
  require_absent_literal images/workspace/Dockerfile 'RAY_TRAIN_V2_ENABLED=1'

  local workspace_stage
  workspace_stage="$(docker_stage images/workspace/Dockerfile workspace-ray256)"
  require_before "$workspace_stage" 'test "${RAY_VERSION}" = "2.56.1"' 'python3 -m pip install'
  for dependency_contract in \
    'python3 -m pip uninstall -y' \
    'azure-cli-core' \
    'conda-content-trust' \
    '      opentelemetry-exporter-otlp \' \
    'opentelemetry-exporter-otlp-proto-grpc' \
    '"google-api-core==2.34.0"' \
    '"googleapis-common-protos==1.75.2"' \
    '"opentelemetry-exporter-otlp==1.44.0"' \
    'import opentelemetry.exporter.prometheus'; do
    if ! grep -Fq -- "$dependency_contract" <<<"$workspace_stage"; then
      echo "Ray 2.56 workspace stage is missing dependency normalization: ${dependency_contract}" >&2
      exit 1
    fi
  done
  require_before "$workspace_stage" 'python3 -m pip uninstall -y' 'python3 -m pip install'
  require_before "$workspace_stage" 'python3 -m pip install' 'python3 -m pip check'

  for required_copy_source in \
    images/workspace/raytrain-launch.py \
    images/workspace/raytrain-managed \
    images/workspace/raytrain_runtime/__init__.py \
    images/workspace/raytrain_runtime/entrypoint.py \
    images/workspace/raytrain_runtime/managed_driver.py; do
    test -f "${root_dir}/${required_copy_source}" || {
      echo "required Ray runtime COPY source is missing: ${required_copy_source}" >&2
      exit 1
    }
  done
  test -x "${root_dir}/images/workspace/raytrain-managed" || {
    echo 'managed Ray Train launcher must be executable' >&2
    exit 1
  }
  "${root_dir}/images/workspace/raytrain-managed" --help >/dev/null
  require_literal images/workspace/raytrain-managed 'from raytrain_runtime.managed_driver import main'
  require_absent_literal images/workspace/raytrain-managed 'placeholder'
  require_literal images/train-pytorch/Dockerfile 'COPY workspace/raytrain-launch.py /usr/local/bin/raytrain-launch'
  require_literal images/workspace/Dockerfile 'COPY raytrain-launch.py /usr/local/bin/raytrain-launch'

  require_literal build-image.sh 'RAY_RUNTIME_VARIANTS'
  require_literal build-image.sh 'pytorch-ray-ddp'
  require_literal build-image.sh 'pytorch-ray-train'
  require_literal build-image.sh 'workspace-ray256'
  require_literal build-image.sh 'RAY_VERSION=${RAY_VERSION_ARG}'
  require_literal build-image.sh "RAY_PRODUCTION_VERSION=\"${production_ray_version}\""
  require_literal build-image.sh 'cleanup_temp_dockerfiles()'
  require_literal build-image.sh 'trap cleanup_temp_dockerfiles EXIT'
  require_literal build-image.sh 'rm -f -- "$dockerfile"'
  if grep -Eq 'rm[[:space:]]+-[^[:space:]]*r' "${root_dir}/build-image.sh"; then
    echo 'runtime builder cleanup must not use recursive removal' >&2
    exit 1
  fi

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
  require_literal helm/ray-train-platform/values.yaml 'canaryTenants: []'
  require_literal helm/ray-train-platform/values.yaml 'supportedEngines: ["ray-ddp"]'
  require_literal helm/ray-train-platform/values.yaml 'supportedEngines: ["ray-train"]'
  require_literal helm/ray-train-platform/templates/backend-deployment.yaml 'RAY_TRAIN_MANAGED_ENABLED'
  require_literal helm/ray-train-platform/templates/backend-deployment.yaml 'RAY_TRAIN_CANARY_ENABLED'
  require_literal helm/ray-train-platform/templates/backend-deployment.yaml 'RAY_TRAIN_CANARY_TENANTS'
  require_literal helm/ray-train-platform/templates/backend-deployment.yaml 'join "," .Values.rayTrain.canaryTenants'

  require_literal scripts/test-delivery-render.sh 'test-ray-runtime-images.sh" --contract-only'
  require_literal .github/workflows/ci.yml 'bash scripts/test-ray-runtime-images.sh --contract-only'
  require_literal docs/superpowers/plans/2026-08-27-ray-train-managed-runtime-upgrade.md 'add `pytorch-ray-train` back to `BUILD_TARGETS=all`'

  local dry_run
  dry_run="$(
    DRY_RUN=true \
    IMAGE_TAG=contract \
    BUILD_TARGETS=all \
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
  local role="$2"

  docker run --rm "$image" python3 -c \
    "import opentelemetry.exporter.prometheus, ray, ray.train, torch; assert ray.__version__ == '${production_ray_version}', ray.__version__; print(ray.__version__, torch.__version__)"
  docker run --rm "$image" sh -eu -c \
    "ray --version | grep -F '${production_ray_version}'; test -x /usr/local/bin/raytrain-launch"

  if [[ "$role" == managed ]]; then
    docker run --rm "$image" sh -eu -c 'test -x /usr/local/bin/raytrain-managed; test "${RAY_TRAIN_V2_ENABLED:-}" = 1'
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

smoke_image "$RAY_DDP_IMAGE" ddp
smoke_image "$RAY_TRAIN_IMAGE" managed
smoke_image "$RAY_WORKSPACE_IMAGE" workspace

echo 'Ray runtime image smoke tests passed'
