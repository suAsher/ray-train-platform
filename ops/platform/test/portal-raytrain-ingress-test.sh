#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"

for manifest in \
  "${root_dir}/deploy/portal/test-dev-raytrain-ingress.yaml" \
  "${root_dir}/deploy/portal/common-raytrain-ingress.yaml"; do
  grep -Fq -- '/raytrain/(api/.*|ray/.*|mlflow/.*)' "${manifest}" || {
    echo "RayTrain Portal proxy allowlist is incomplete: ${manifest}" >&2
    exit 1
  }
  if grep -Fq -- '/raytrain/(.*)' "${manifest}"; then
    echo "RayTrain Portal proxy must not capture SPA routes: ${manifest}" >&2
    exit 1
  fi
  grep -Fq -- 'nginx.ingress.kubernetes.io/rewrite-target: /$1' "${manifest}" || {
    echo "RayTrain Portal proxy rewrite target changed: ${manifest}" >&2
    exit 1
  }
done

echo 'Portal RayTrain Ingress contract verified'
