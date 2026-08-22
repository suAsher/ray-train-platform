#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
BLOCKS="${ROOT}/ops/dns/coredns-volcengine-forwarding.conf"
DEPLOY="${ROOT}/ops/dns/deploy-coredns-split-dns.sh"
NODE_DNS="${ROOT}/ops/storage/shanghai-data-transfer/50-configure-node-split-dns.sh"

test -s "$BLOCKS"
test -x "$DEPLOY"
bash -n "$DEPLOY"
bash -n "$NODE_DNS"

for zone in ivolces.com volcengineapi.com tos-cn-shanghai.volces.com; do
  grep -F "${zone}:53" "$BLOCKS" >/dev/null
done

test "$(grep -Fc 'forward . 100.96.0.2 100.96.0.3' "$BLOCKS")" -eq 3
! grep -Eq '192\.168\.11(0\.61|1\.63)' "$BLOCKS"

grep -F 'get configmap "$CONFIGMAP"' "$DEPLOY" >/dev/null
grep -F 'deployment/"$DEPLOYMENT"' "$DEPLOY" >/dev/null
grep -F 'CoreDNS rollout failed; restoring the previous Corefile' "$DEPLOY" >/dev/null
grep -F 'apply_corefile "$previous"' "$DEPLOY" >/dev/null
grep -F -- '--check' "$DEPLOY" >/dev/null
grep -F -- '--apply' "$DEPLOY" >/dev/null
grep -F -- '--revert' "$DEPLOY" >/dev/null

grep -F '~tos-cn-shanghai.volces.com' "$NODE_DNS" >/dev/null

echo "split DNS contract passed"
