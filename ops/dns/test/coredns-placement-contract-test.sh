#!/usr/bin/env bash
set -euo pipefail

readonly ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly SCRIPT="${ROOT}/ops/dns/reconcile-coredns-placement.sh"
readonly MIGRATION="${ROOT}/ops/ha/migrate-vci-control-plane.sh"
readonly WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

[[ -x "$SCRIPT" ]] || {
  echo "missing executable CoreDNS placement reconciler: $SCRIPT" >&2
  exit 1
}

grep -Fq 'COREDNS_CPU_REQUEST:-250m' "$SCRIPT"
grep -Fq 'COREDNS_MEMORY_REQUEST:-256Mi' "$SCRIPT"
grep -Fq 'COREDNS_REPLICAS:-2' "$SCRIPT"
grep -Fq 'preferredDuringSchedulingIgnoredDuringExecution' "$SCRIPT"
grep -Fq -- '--type strategic' "$SCRIPT"

cat >"${WORKDIR}/desired.json" <<'JSON'
{"spec":{"replicas":2,"template":{"spec":{"nodeSelector":{},"affinity":{"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":{"nodeSelectorTerms":[{"matchExpressions":[{"key":"platform.wellspiking.ai/pool","operator":"In","values":["control-plane"]}]},{"matchExpressions":[{"key":"platform.wellspiking.ai/gpu-pool","operator":"In","values":["production"]}]}]},"preferredDuringSchedulingIgnoredDuringExecution":[{"weight":100,"preference":{"matchExpressions":[{"key":"platform.wellspiking.ai/pool","operator":"In","values":["control-plane"]}]}}]}},"containers":[{"name":"coredns","resources":{"requests":{"cpu":"250m","memory":"256Mi"},"limits":{"cpu":"2","memory":"1Gi"}}}]}}}}
JSON

cat >"${WORKDIR}/pinned.json" <<'JSON'
{"spec":{"replicas":2,"template":{"spec":{"nodeSelector":{"platform.wellspiking.ai/pool":"control-plane"},"containers":[{"name":"coredns","resources":{"requests":{"cpu":"2","memory":"4Gi"},"limits":{"cpu":"2","memory":"4Gi"}}}]}}}}
JSON

cp "${WORKDIR}/desired.json" "${WORKDIR}/state.json"

cat >"${WORKDIR}/kubectl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

if [[ " $* " == *" get deployment coredns "* ]]; then
  cat "$STATE_FILE"
  exit 0
fi
if [[ " $* " == *" patch deployment coredns "* ]]; then
  printf '%s\n' "$*" >>"$CALL_LOG"
  cp "$DESIRED_FILE" "$STATE_FILE"
  exit 0
fi
if [[ " $* " == *" rollout status deployment/coredns "* ]]; then
  printf '%s\n' "$*" >>"$CALL_LOG"
  exit 0
fi
echo "unexpected kubectl invocation: $*" >&2
exit 2
SH
chmod +x "${WORKDIR}/kubectl"

export STATE_FILE="${WORKDIR}/state.json"
export DESIRED_FILE="${WORKDIR}/desired.json"
export CALL_LOG="${WORKDIR}/calls.log"

KUBECTL_BIN="${WORKDIR}/kubectl" "$SCRIPT" --check

cp "${WORKDIR}/pinned.json" "$STATE_FILE"
if KUBECTL_BIN="${WORKDIR}/kubectl" "$SCRIPT" --check >/dev/null 2>&1; then
  echo 'placement check unexpectedly accepted a CPU-only CoreDNS selector' >&2
  exit 1
fi

KUBECTL_BIN="${WORKDIR}/kubectl" "$SCRIPT" --apply
grep -Fq 'patch deployment coredns' "$CALL_LOG"
grep -Fq 'rollout status deployment/coredns' "$CALL_LOG"
KUBECTL_BIN="${WORKDIR}/kubectl" "$SCRIPT" --check

grep -Fq 'reconcile-coredns-placement.sh" --apply' "$MIGRATION"
if grep -Fq 'patch_deployment_to_cpu kube-system coredns 2' "$MIGRATION"; then
  echo 'VCI retirement would pin CoreDNS back to the CPU-only pool' >&2
  exit 1
fi

echo 'CoreDNS shared-node placement contract verified'
