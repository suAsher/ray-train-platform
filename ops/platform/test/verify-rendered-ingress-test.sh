#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly VERIFY_SCRIPT="${ROOT_DIR}/ops/platform/verify.sh"

grep -Fq 'rendered_ingress_names' "$VERIFY_SCRIPT" || {
  echo 'verify must discover ingress names from the rendered profile' >&2
  exit 1
}
grep -Fq 'get ingress "$ingress_name"' "$VERIFY_SCRIPT" || {
  echo 'verify must check every rendered ingress by name' >&2
  exit 1
}
if grep -Fq 'get ingress ray-platform-intranet-alb-ingress' "$VERIFY_SCRIPT"; then
  echo 'verify must not hard-code the legacy HTTP ingress name' >&2
  exit 1
fi

echo 'rendered ingress verification contract verified'
