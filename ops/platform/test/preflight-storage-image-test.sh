#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly PREFLIGHT="${ROOT_DIR}/ops/platform/preflight.sh"
readonly PREFIX_INIT_MANIFEST="${ROOT_DIR}/ops/storage/shanghai-data-transfer/00-prefix-init.yaml"

grep -Fq 'FSX_SMOKE_IMAGE' "$PREFLIGHT" || {
  echo 'FSX preflight must support an explicit smoke image override' >&2
  exit 1
}
grep -Fq 'harbor.wellspiking.ai/hub/library/busybox:1.36' "$PREFLIGHT" || {
  echo 'FSX preflight must default to a public, independent smoke image' >&2
  exit 1
}
if grep -Fq 'smoke_image="$(rendered_env_value "$rendered" WORKSPACE_IMAGE)"' "$PREFLIGHT"; then
  echo 'FSX preflight must not depend on the private workspace image' >&2
  exit 1
fi

grep -Fq 'imagePullSecrets:' "$PREFIX_INIT_MANIFEST" || {
  echo 'TOS prefix initializer must use the platform registry pull Secret' >&2
  exit 1
}
grep -Fq 'name: harbor-registry' "$PREFIX_INIT_MANIFEST" || {
  echo 'TOS prefix initializer must name harbor-registry as its pull Secret' >&2
  exit 1
}

echo 'FSX preflight storage image contract verified'
