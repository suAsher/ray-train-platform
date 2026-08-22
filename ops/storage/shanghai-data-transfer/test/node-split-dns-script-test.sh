#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../../.." && pwd)"
SCRIPT="${ROOT}/ops/storage/shanghai-data-transfer/50-configure-node-split-dns.sh"

[[ -x "$SCRIPT" ]] || { echo "missing executable: $SCRIPT" >&2; exit 1; }
"$SCRIPT" --help >/dev/null

grep -F '100.96.0.2' "$SCRIPT" >/dev/null
grep -F '100.96.0.3' "$SCRIPT" >/dev/null
grep -F 'TOS_API_TEST_HOST' "$SCRIPT" >/dev/null
grep -F 'tos-cn-shanghai.ivolces.com' "$SCRIPT" >/dev/null
grep -F 'shanghai-data-transfer.tos-cn-shanghai.ivolces.com' "$SCRIPT" >/dev/null
grep -F '~tos-cn-shanghai.ivolces.com' "$SCRIPT" >/dev/null
grep -F '~tos-s3-cn-shanghai.ivolces.com' "$SCRIPT" >/dev/null
grep -F '~volcengineapi.com' "$SCRIPT" >/dev/null
grep -F 'systemd-resolved' "$SCRIPT" >/dev/null
grep -F -- '--revert' "$SCRIPT" >/dev/null

echo "node split-DNS script contract passed"
