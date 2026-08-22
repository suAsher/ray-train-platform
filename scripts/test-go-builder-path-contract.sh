#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

for dockerfile in backend/Dockerfile backend/Dockerfile.spk-rayjob; do
  if ! grep -Fq 'ENV PATH=/usr/local/go/bin:$PATH' "${root_dir}/${dockerfile}"; then
    echo "Go builder PATH contract missing: ${dockerfile}" >&2
    exit 1
  fi
done

echo 'Go builder PATH contract verified'
