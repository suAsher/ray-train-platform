#!/usr/bin/env bash
set -euo pipefail

readonly ops_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly test_root="$(mktemp -d)"
trap 'rm -f -- "${test_root}/calls" "${test_root}/manifest" "${test_root}/output"; rmdir "${test_root}"' EXIT
export VERIFY_TEST_ROOT="${test_root}"

# No real Kubernetes or SSH access is permitted in this test.
kubectl() {
  printf '%s\n' "$*" >>"${VERIFY_TEST_ROOT}/calls"
  case "$*" in
    'get node '*) printf '%s' "${MOCK_CORDON-true}" ;;
    'get storageclass '*) return 0 ;;
    'apply -f '*) cp "$3" "${VERIFY_TEST_ROOT}/manifest"; return "${MOCK_APPLY_STATUS:-77}" ;;
    *'get pvc '*data1*) printf 'pv1' ;;
    *'get pvc '*data2*) printf 'pv2' ;;
    'get pv pv1 -o '*) printf '/data1/ray-cache/pvc-11111111-1111-4111-8111-111111111111_test_smoke' ;;
    'get pv pv2 -o '*) printf '/data2/ray-cache/pvc-22222222-2222-4222-8222-222222222222_test_smoke' ;;
    'get pv '*) return "${MOCK_DELETE_STATUS:-1}" ;;
    *'get pod '*) printf '172.28.1.229' ;;
    'wait --for=delete '*) return "${MOCK_DELETE_STATUS:-0}" ;;
    *'wait pod '*|*'exec '*) return 0 ;;
    *'delete pod '*|*'delete pvc '*) return 0 ;;
    *) return 99 ;;
  esac
}
ssh() {
  printf 'ssh %s\n' "$*" >>"${VERIFY_TEST_ROOT}/calls"
  [[ "${MOCK_APPLY_STATUS:-77}" == 0 ]]
}
export -f kubectl ssh

reject_args() {
  : >"${test_root}/calls"
  if bash "${ops_dir}/verify-dual.sh" "$@" >"${test_root}/output" 2>&1; then
    echo "accepted unsafe arguments: $*" >&2; exit 1
  fi
  [[ ! -s "${test_root}/calls" ]] || { echo 'invalid arguments reached Kubernetes' >&2; exit 1; }
}
reject_args
reject_args --node
reject_args --all
reject_args --node 172.28.1.229 --node old-node
for node in '-node' 'a..b' 'a/b' 'a;echo' 'A' 'a&b' 'a.'; do
  reject_args --node "${node}"
done

for cordon in false ''; do
  : >"${test_root}/calls"
  if MOCK_CORDON="${cordon}" bash "${ops_dir}/verify-dual.sh" --node 172.28.1.229 >"${test_root}/output" 2>&1; then
    echo 'accepted schedulable node' >&2; exit 1
  fi
  ! grep -q 'apply\|delete' "${test_root}/calls"
  grep -q 'cordon' "${test_root}/output"
done

: >"${test_root}/calls"
status=0
bash "${ops_dir}/verify-dual.sh" --node 172.28.1.229 >"${test_root}/output" 2>&1 || status=$?
[[ "${status}" == 77 ]] || { cat "${test_root}/output"; echo 'cordoned target did not reach smoke apply' >&2; exit 1; }
grep -Fq 'kubernetes.io/hostname: 172.28.1.229' "${test_root}/manifest"
grep -Fq 'key: node.kubernetes.io/unschedulable' "${test_root}/manifest"
grep -Fq 'effect: NoSchedule' "${test_root}/manifest"
[[ "$(grep -c 'key:' "${test_root}/manifest")" == 1 ]]
[[ "$(grep -c 'delete pod ' "${test_root}/calls")" == 1 ]]
[[ "$(grep -c 'delete pvc ' "${test_root}/calls")" == 2 ]]
! grep -Eq '172.28.1.23[23]|--all|uncordon|label|taint' "${test_root}/calls"

# Failed API observation (auth/network/timeout) must never mean PV deletion succeeded.
: >"${test_root}/calls"
status=0
MOCK_APPLY_STATUS=0 MOCK_DELETE_STATUS=42 bash "${ops_dir}/verify-dual.sh" --node 172.28.1.229 >"${test_root}/output" 2>&1 || status=$?
[[ "${status}" == 42 ]] || { echo "PV observation error was not propagated: ${status}" >&2; exit 1; }
! grep -q '^ssh ' "${test_root}/calls"
! grep -q 'cleanup verified' "${test_root}/output"

: >"${test_root}/calls"
MOCK_APPLY_STATUS=0 MOCK_DELETE_STATUS=0 bash "${ops_dir}/verify-dual.sh" --node 172.28.1.229 >"${test_root}/output" 2>&1
grep -Fxq 'wait --for=delete pv/pv1 --timeout=120s' "${test_root}/calls"
grep -Fxq 'wait --for=delete pv/pv2 --timeout=120s' "${test_root}/calls"
[[ "$(grep -c '^ssh ' "${test_root}/calls")" == 2 ]]
grep -q 'cleanup verified' "${test_root}/output"
echo 'dual verification explicit cordoned node contract verified'
