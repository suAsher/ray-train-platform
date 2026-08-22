#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly TEMPLATE="${ROOT_DIR}/helm/ray-train-platform/templates/postgres.yaml"

grep -Fq '{{- with .Values.global.imagePullSecrets }}' "$TEMPLATE" || {
  echo 'standalone PostgreSQL must consume global imagePullSecrets' >&2
  exit 1
}
grep -Fq 'imagePullSecrets:' "$TEMPLATE" || {
  echo 'standalone PostgreSQL must render imagePullSecrets' >&2
  exit 1
}

echo 'PostgreSQL pull-secret template contract verified'
