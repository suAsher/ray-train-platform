#!/usr/bin/env bash
set -euo pipefail

dockerfile="${1:-$(cd "$(dirname "$0")" && pwd)/Dockerfile}"

grep -Fq 'apt-get install' "$dockerfile"
grep -Fq 'git curl less nano ca-certificates' "$dockerfile"
grep -Fq 'repo.wellspiking.ai/repository/ubuntu-jammy' "$dockerfile"
grep -Fq 'repo.wellspiking.ai/repository/conda-main' "$dockerfile"
grep -Fq 'repo.wellspiking.ai/repository/conda-forge' "$dockerfile"
grep -Fq 'ENV PIP_CACHE_DIR=/workspace/.cache/pip' "$dockerfile"
grep -Fq 'ENV PYTHONPYCACHEPREFIX=/workspace/.cache/pycache' "$dockerfile"
grep -Fq 'chown -R ray:users /home/ray /workspace' "$dockerfile"

if grep -Fq -- '--no-cache-dir' "$dockerfile"; then
  echo 'workspace image must preserve the runtime pip cache' >&2
  exit 1
fi
