#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly PREFLIGHT="${ROOT_DIR}/ops/platform/preflight.sh"

grep -Fq 'rendered_defines_ingress_class' "$PREFLIGHT" || {
  echo 'preflight must allow an IngressClass rendered by this Helm release' >&2
  exit 1
}
grep -Fq 'managed by this release' "$PREFLIGHT" || {
  echo 'preflight must explain why a Helm-managed IngressClass is not pre-existing' >&2
  exit 1
}

echo 'managed IngressClass preflight contract verified'
