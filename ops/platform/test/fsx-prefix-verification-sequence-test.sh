#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly VERIFIER="${ROOT_DIR}/ops/storage/shanghai-data-transfer/41-verify-irsa-prefix-mount.sh"

grep -Fq 'deferred_smoke_b_pod' "$VERIFIER" || {
  echo 'FSX verifier must defer the second mount pod until the first completes' >&2
  exit 1
}
grep -Fq 'delete pod/smoke-a --wait=true' "$VERIFIER" || {
  echo 'FSX verifier must release the first mount before the second is scheduled' >&2
  exit 1
}
grep -Fq 'apply -f "$deferred_smoke_b_pod"' "$VERIFIER" || {
  echo 'FSX verifier must apply the deferred second mount pod after release' >&2
  exit 1
}
grep -Fq 'wait_for_smoke smoke-a' "$VERIFIER" || {
  echo 'FSX verifier must use the bounded cold-start retry for smoke-a' >&2
  exit 1
}
grep -Fq 'wait_for_smoke smoke-b' "$VERIFIER" || {
  echo 'FSX verifier must use the bounded cold-start retry for smoke-b' >&2
  exit 1
}
grep -Fq 'for attempt in 1 2' "$VERIFIER" || {
  echo 'FSX verifier must retry a cold FSX mount exactly once' >&2
  exit 1
}

echo 'FSX prefix verification sequence contract verified'
