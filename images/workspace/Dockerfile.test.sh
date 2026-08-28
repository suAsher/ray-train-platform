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
grep -Fq 'AS workspace-legacy' "$dockerfile"
grep -Fq 'AS workspace-ray256' "$dockerfile"
grep -Fq 'ARG RAY_VERSION=2.56.1' "$dockerfile"
grep -Fq 'test "${RAY_VERSION}" = "2.56.1"' "$dockerfile"
grep -Fq 'ray[default,train,tune]==${RAY_VERSION}' "$dockerfile"
grep -Fq 'import opentelemetry.exporter.prometheus' "$dockerfile"
grep -Fq 'python3 -m pip check' "$dockerfile"

if grep -Fq 'raytrain-managed' "$dockerfile"; then
  echo 'buildable workspace image must not depend on the Task 9 managed launcher' >&2
  exit 1
fi

if grep -Fq -- '--no-cache-dir' "$dockerfile"; then
  echo 'workspace image must preserve the runtime pip cache' >&2
  exit 1
fi
