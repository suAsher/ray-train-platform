#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
PROFILE="${ROOT_DIR}/deploy/profiles/vke-cpu-ha.yaml"
ALLOY_VALUES="${ROOT_DIR}/ops/observability/alloy/20-values-cpu-ha.yaml"
DELIVERY_TEST="${ROOT_DIR}/scripts/test-delivery-render.sh"
VERIFY_SCRIPT="${ROOT_DIR}/ops/observability/loki/30-verify-loki.sh"

grep -Fq 'lokiURL: http://loki-cpu.loki.svc.cluster.local:3100' "${PROFILE}"
grep -Fq 'value: http://loki-cpu.loki.svc.cluster.local:3100/loki/api/v1/push' "${ALLOY_VALUES}"
grep -Fq 'LOKI_VERIFY_SERVICE:-${release}' "${VERIFY_SCRIPT}"
grep -Fq 'LOKI_VERIFY_SERVICE_PORT:-3100' "${VERIFY_SCRIPT}"
grep -Fq 'ops/observability/loki/test/direct-client-paths-test.sh' "${DELIVERY_TEST}"

if grep -Fq 'loki-cpu-gateway.loki.svc.cluster.local' "${PROFILE}"; then
  echo 'backend log reads must not depend on the Loki nginx gateway DNS resolver' >&2
  exit 1
fi

if grep -Fq 'loki-cpu-gateway.loki.svc.cluster.local/loki/api/v1/push' "${ALLOY_VALUES}"; then
  echo 'Alloy log writes must not depend on the Loki nginx gateway DNS resolver' >&2
  exit 1
fi
