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

grep -F 'IDC_DNS_PRIMARY="${IDC_DNS_PRIMARY:-192.168.110.61}"' "$DEPLOY" >/dev/null
grep -F 'IDC_DNS_SECONDARY="${IDC_DNS_SECONDARY:-192.168.111.63}"' "$DEPLOY" >/dev/null
grep -F 'rewrite_root_forwarder' "$DEPLOY" >/dev/null
grep -F 'require_managed_root_source' "$DEPLOY" >/dev/null
grep -F 'forward . ${IDC_DNS_PRIMARY} ${IDC_DNS_SECONDARY}' "$DEPLOY" >/dev/null
grep -F 'forward . /etc/resolv.conf' "$DEPLOY" >/dev/null

grep -F 'get configmap "$CONFIGMAP"' "$DEPLOY" >/dev/null
grep -F 'deployment/"$DEPLOYMENT"' "$DEPLOY" >/dev/null
grep -F 'CoreDNS rollout failed; restoring the previous Corefile' "$DEPLOY" >/dev/null
grep -F 'apply_corefile "$previous"' "$DEPLOY" >/dev/null
grep -F -- '--check' "$DEPLOY" >/dev/null
grep -F -- '--apply' "$DEPLOY" >/dev/null
grep -F -- '--revert' "$DEPLOY" >/dev/null

grep -F '~tos-cn-shanghai.volces.com' "$NODE_DNS" >/dev/null

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
mock_corefile="${workdir}/Corefile"
mock_bin="${workdir}/bin"
mkdir -p "$mock_bin"

cat >"$mock_corefile" <<'EOF'
.:53 {
    errors
    health
    kubernetes cluster.local in-addr.arpa ip6.arpa
    forward . /etc/resolv.conf {
        max_concurrent 10000
    }
    cache 30
}
EOF

cat >"${mock_bin}/kubectl" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
case " $* " in
  *" get configmap "*)
    cat "$MOCK_COREFILE"
    ;;
  *" patch configmap "*)
    patch_file=""
    for ((index = 1; index <= $#; index++)); do
      if [[ "${!index}" == "--patch-file" ]]; then
        next=$((index + 1))
        patch_file="${!next}"
      fi
    done
    test -n "$patch_file"
    jq -r '.data.Corefile' "$patch_file" >"${MOCK_COREFILE}.next"
    mv "${MOCK_COREFILE}.next" "$MOCK_COREFILE"
    ;;
  *" rollout restart deployment/"*|*" rollout status deployment/"*)
    ;;
  *)
    echo "unexpected kubectl invocation: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "${mock_bin}/kubectl"

MOCK_COREFILE="$mock_corefile" PATH="${mock_bin}:${PATH}" bash "$DEPLOY" --apply >/dev/null
grep -F 'forward . 192.168.110.61 192.168.111.63 {' "$mock_corefile" >/dev/null
test "$(grep -Fc 'forward . 100.96.0.2 100.96.0.3' "$mock_corefile")" -eq 3
MOCK_COREFILE="$mock_corefile" PATH="${mock_bin}:${PATH}" bash "$DEPLOY" --check >/dev/null
MOCK_COREFILE="$mock_corefile" PATH="${mock_bin}:${PATH}" bash "$DEPLOY" --revert >/dev/null
grep -F 'forward . /etc/resolv.conf {' "$mock_corefile" >/dev/null
! grep -F '# BEGIN ray-train-platform managed Volcengine DNS forwarding' "$mock_corefile" >/dev/null

sed 's#forward \. /etc/resolv.conf#forward . 10.10.10.10#' "$mock_corefile" >"${mock_corefile}.custom"
mv "${mock_corefile}.custom" "$mock_corefile"
if MOCK_COREFILE="$mock_corefile" PATH="${mock_bin}:${PATH}" bash "$DEPLOY" --apply >/dev/null 2>&1; then
  echo "split DNS apply unexpectedly replaced an unmanaged root forwarder" >&2
  exit 1
fi
grep -F 'forward . 10.10.10.10 {' "$mock_corefile" >/dev/null

echo "split DNS contract passed"
