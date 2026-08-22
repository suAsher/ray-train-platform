#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly VALUES="${ROOT_DIR}/ops/observability/loki/20-values-cpu-ha.yaml"

grep -Fxq 'fullnameOverride: loki-cpu' "$VALUES" || {
  echo 'CPU Loki must use an independent fullnameOverride while the old release is retained' >&2
  exit 1
}
grep -Fxq 'nameOverride: loki-cpu' "$VALUES" || {
  echo 'CPU Loki must use an independent nameOverride for the runtime ConfigMap' >&2
  exit 1
}
grep -Fxq '  configObjectName: loki-cpu' "$VALUES" || {
  echo 'CPU Loki must use an independent config object name while the old release is retained' >&2
  exit 1
}
grep -Fxq '  generatedConfigObjectName: loki-cpu' "$VALUES" || {
  echo 'CPU Loki must create an independent config object while the old release is retained' >&2
  exit 1
}
if grep -Fq 'initialize-wal-permissions' "$VALUES" || grep -Fq 'runAsUser: 0' "$VALUES"; then
  echo 'CPU Loki must not require a root init container' >&2
  exit 1
fi
grep -Fq '  podSecurityContext:' "$VALUES" || {
  echo 'CPU Loki must explicitly configure non-root EBS volume ownership' >&2
  exit 1
}
grep -Fq '    runAsNonRoot: true' "$VALUES" || {
  echo 'CPU Loki must run with a non-root pod security context' >&2
  exit 1
}
grep -Fq '    resolver: kube-dns.kube-system.svc.cluster.local valid=30s ipv6=off' "$VALUES" || {
  echo 'CPU Loki gateway must use IPv4-only Kubernetes DNS resolution' >&2
  exit 1
}
if [[ "$(grep -Fc 'app.kubernetes.io/name: loki-cpu' "$VALUES")" -lt 4 ]]; then
  echo 'CPU Loki anti-affinity and topology spread selectors must match its overridden pod labels' >&2
  exit 1
fi

echo 'CPU Loki fullname contract verified'
