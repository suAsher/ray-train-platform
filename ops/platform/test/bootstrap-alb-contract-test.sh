#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly BOOTSTRAP="${ROOT_DIR}/ops/platform/bootstrap-alb.sh"

[[ -x "$BOOTSTRAP" ]] || {
  echo 'missing executable private ALB bootstrap script' >&2
  exit 1
}
grep -Fq -- '--set albInstance.enabled=true' "$BOOTSTRAP" || {
  echo 'ALB bootstrap must render only the opted-in network resources' >&2
  exit 1
}
grep -Fq 'extract_kind "ALBInstance"' "$BOOTSTRAP" || {
  echo 'ALB bootstrap must apply ALBInstance before IngressClass' >&2
  exit 1
}
grep -Fq 'extract_kind "IngressClass"' "$BOOTSTRAP" || {
  echo 'ALB bootstrap must apply IngressClass after the ALB is ready' >&2
  exit 1
}
grep -Fq '$0 ~ /^kind: /' "$BOOTSTRAP" || {
  echo 'ALB bootstrap must distinguish a top-level kind from IngressClass parameters' >&2
  exit 1
}

echo 'private ALB bootstrap contract verified'
