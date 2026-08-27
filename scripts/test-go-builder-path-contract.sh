#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

for dockerfile in backend/Dockerfile backend/Dockerfile.spk-rayjob; do
  if ! grep -Fq 'ENV PATH=/usr/local/go/bin:$PATH' "${root_dir}/${dockerfile}"; then
    echo "Go builder PATH contract missing: ${dockerfile}" >&2
    exit 1
  fi
done

bash -n "${root_dir}/build-image.sh"

matrix_output="$(
  DRY_RUN=true \
  IMAGE_TAG=contract \
  BUILD_TARGETS=workspace,train-pytorch,pytorch-ray-ddp,pytorch-ray-train,workspace-ray256 \
  bash "${root_dir}/build-image.sh"
)"

# A caller may carry an unrelated RAY_VERSION variable while building the API;
# only the production Ray runtime targets own and validate this setting.
RAY_VERSION=2.58.0 DRY_RUN=true IMAGE_TAG=contract BUILD_TARGETS=backend \
  bash "${root_dir}/build-image.sh" >/dev/null

if RAY_VERSION=2.58.0 DRY_RUN=true IMAGE_TAG=contract BUILD_TARGETS=pytorch-ray-ddp \
  bash "${root_dir}/build-image.sh" >/dev/null 2>&1; then
  echo 'production Ray runtime target accepted a non-production Ray version' >&2
  exit 1
fi

for expected in \
  'ray-workspace:contract' \
  'ray-train-pytorch:contract' \
  'ray-train-pytorch-ray-ddp:2.56.1-contract' \
  'ray-train-pytorch-ray-train:2.56.1-contract' \
  'ray-workspace-ray256:2.56.1-contract'; do
  if ! grep -Fq "$expected" <<<"$matrix_output"; then
    echo "runtime build matrix is missing ${expected}" >&2
    exit 1
  fi
done

echo 'Go builder PATH contract verified'
