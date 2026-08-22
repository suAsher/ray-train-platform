#!/usr/bin/env bash
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
for dockerfile in "$root/images/train-pytorch/Dockerfile" "$root/images/workspace/Dockerfile"; do
  grep -Fq 'mlflow-skinny==3.14.0' "$dockerfile" || {
    echo "missing pinned MLflow client in ${dockerfile}" >&2
    exit 1
  }
done
echo 'MLflow client image contract verified'
