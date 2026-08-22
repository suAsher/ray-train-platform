#!/usr/bin/env bash
set -euo pipefail

readonly VALUES_FILE="${1:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)/helm/ray-train-platform/values.yaml}"

matches="$(grep -Fc 'ingress.vke.volcengine.com/loadbalancer-pass-through: "true"' "$VALUES_FILE")"
if [ "$matches" -lt 2 ]; then
  echo 'both HTTP and HTTPS ALB ingress defaults must enable Pod pass-through for ClusterIP services' >&2
  exit 1
fi
