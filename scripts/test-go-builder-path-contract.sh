#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
trap 'rm -rf -- "$test_root"' EXIT

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
  BUILD_TARGETS=all \
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
  'ray-workspace-ray256:2.56.1-contract'; do
  if ! grep -Fq "$expected" <<<"$matrix_output"; then
    echo "runtime build matrix is missing ${expected}" >&2
    exit 1
  fi
done

if grep -Fq 'ray-train-pytorch-ray-train:' <<<"$matrix_output"; then
  echo 'default all matrix must remain buildable before Task 9 supplies raytrain-managed' >&2
  exit 1
fi

assert_managed_preflight() {
  local targets="$1"
  local output_file="${test_root}/managed-output-${2}"
  if DRY_RUN=true IMAGE_TAG=contract BUILD_TARGETS="$targets" \
    bash "${root_dir}/build-image.sh" >"$output_file" 2>&1; then
    echo "managed target list bypassed launcher preflight: ${targets}" >&2
    exit 1
  fi
  grep -Fq 'pytorch-ray-train requires images/workspace/raytrain-managed from Task 9' "$output_file"
  if grep -Eq -- '(^--- Building|docker (build|push))' "$output_file"; then
    echo "managed target validation started a build before completing preflight: ${targets}" >&2
    exit 1
  fi
  if grep -Fq 'unbound variable' "$output_file"; then
    echo 'managed preflight triggered a secondary cleanup trap failure' >&2
    exit 1
  fi
}

assert_managed_preflight 'pytorch-ray-train' single
assert_managed_preflight 'pytorch-ray-ddp,pytorch-ray-train' ddp-first
assert_managed_preflight 'workspace-ray256,pytorch-ray-train' workspace-first

# Force the legacy Docker path to fail after creating its temporary Dockerfile.
# The EXIT trap in build-image.sh must remove that exact file on every exit.
fake_bin="${test_root}/bin"
docker_tmp="${test_root}/docker-tmp"
mkdir -p "$fake_bin" "$docker_tmp"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'if [[ "${1:-}" == info ]]; then exit 0; fi' \
  'if [[ "${1:-}" == build ]]; then exit 42; fi' \
  'exit 0' >"${fake_bin}/docker"
chmod 0755 "${fake_bin}/docker"
if PATH="${fake_bin}:${PATH}" TMPDIR="$docker_tmp" BUILD_TARGETS=backend USE_BUILDX=false DRY_RUN=false \
  bash "${root_dir}/build-image.sh" >/dev/null 2>&1; then
  echo 'fake Docker build unexpectedly succeeded' >&2
  exit 1
fi
if find "$docker_tmp" -mindepth 1 -print -quit | grep -q .; then
  echo 'temporary Dockerfile leaked after failed legacy build' >&2
  exit 1
fi

echo 'Go builder PATH contract verified'
