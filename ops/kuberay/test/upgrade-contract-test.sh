#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly ops_dir="${root_dir}/ops/kuberay"
readonly preflight="${ops_dir}/preflight-upgrade.sh"
readonly backup="${ops_dir}/backup.sh"
readonly upgrade="${ops_dir}/upgrade-1.6.2.sh"
readonly verify="${ops_dir}/verify.sh"

fail() {
  echo "KubeRay upgrade contract failed: $*" >&2
  exit 1
}

require_file() {
  [[ -f "$1" ]] || fail "missing file: $1"
}

require_in() {
  grep -Fq -- "$2" "$1" || fail "$(basename "$1") missing: $2"
}

reject_in() {
  if grep -Fq -- "$2" "$1"; then
    fail "$(basename "$1") contains forbidden operation: $2"
  fi
}

for script in "$preflight" "$backup" "$upgrade" "$verify"; do
  require_file "$script"
  require_in "$script" 'set -euo pipefail'
  require_in "$script" 'kubectl config current-context'
  require_in "$script" 'CONFIRM_KUBE_CONTEXT'
  bash -n "$script"
done

for expected in \
  'kubectl get rayjobs.ray.io --all-namespaces' \
  'kubectl get rayclusters.ray.io --all-namespaces' \
  'ray.io/dev-workspace=true' \
  'SUCCEEDED|FAILED|STOPPED' \
  'kueue-controller-manager' \
  'clusterqueues.kueue.x-k8s.io' \
  'operator replicas are not ready' \
  'kubectl auth can-i'; do
  require_in "$preflight" "$expected"
done

for expected in \
  'backup target must be an absolute path' \
  'operator-deployment.yaml' \
  'operator-helm-values.yaml' \
  'ray-resources.yaml' \
  'kueue-resources.yaml' \
  'rayjobs.ray.io,rayclusters.ray.io,rayservices.ray.io'; do
  require_in "$backup" "$expected"
done

for expected in \
  'KUBERAY_TARGET_VERSION="1.6.2"' \
  'CONFIRM_KUBERAY_UPGRADE' \
  'kubectl replace -k' \
  'helm upgrade' \
  '"${KUBERAY_RELEASE}"' \
  'preflight-upgrade.sh' \
  'verify.sh'; do
  require_in "$upgrade" "$expected"
done
for forbidden in 'kubectl delete' 'kubectl patch rayjobs' 'kubectl set image' 'ray runtime image'; do
  reject_in "$upgrade" "$forbidden"
done

for expected in \
  'served/storage' \
  'kubectl rollout status' \
  'readyReplicas' \
  '/apis/ray.io/v1' \
  'webhook' \
  'rayjobs.ray.io,rayclusters.ray.io,rayservices.ray.io'; do
  require_in "$verify" "$expected"
done

temporary="$(mktemp -d)"
trap 'rm -rf "${temporary}"' EXIT
mkdir -p "${temporary}/bin"
readonly fixture_log="${temporary}/commands.log"

cat >"${temporary}/bin/kubectl" <<'FIXTURE'
#!/usr/bin/env bash
set -euo pipefail
printf 'kubectl %s\n' "$*" >>"${FIXTURE_LOG}"
command_line=" $* "
case "${command_line}" in
  *' config current-context '*) printf '%s\n' "${FIXTURE_CURRENT_CONTEXT:-fixture-context}" ;;
  *' version --request-timeout='*) printf 'Client Version: fixture\nServer Version: fixture\n' ;;
  *' auth can-i '*) printf 'yes\n' ;;
  *' api-resources '*) printf 'rayjobs ray.io/v1 true RayJob\n' ;;
  *' get --raw /apis/ray.io/v1 '*) printf '{"kind":"APIResourceList"}\n' ;;
  *' get rayjobs.ray.io --all-namespaces '*) printf '%b' "${FIXTURE_RAYJOBS:-}" ;;
  *' get rayclusters.ray.io --all-namespaces '*'-l ray.io/dev-workspace=true'*) printf '%b' "${FIXTURE_DEBUG_CLUSTERS:-}" ;;
  *' get rayclusters.ray.io --all-namespaces '*) printf '%b' "${FIXTURE_RAYCLUSTERS:-}" ;;
  *' get crd rayjobs.ray.io '*|*' get crd rayclusters.ray.io '*|*' get crd rayservices.ray.io '*)
    if [[ "${command_line}" == *'jsonpath='* ]]; then
      printf '%b\n' "${FIXTURE_CRD_VERSIONS:-v1 true true}"
    else
      printf 'apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\n'
    fi
    ;;
  *' get crd clusterqueues.kueue.x-k8s.io '*|*' get crd localqueues.kueue.x-k8s.io '*) printf 'crd.fixture\n' ;;
  *' get deployment kueue-controller-manager '*)
    if [[ "${FIXTURE_KUEUE_HEALTHY:-1}" == 1 ]]; then printf '2 2 2\n'; else printf '2 1 1\n'; fi
    ;;
  *' get deployment '*'-o jsonpath='*)
    if [[ "${FIXTURE_OPERATOR_HEALTHY:-1}" == 1 ]]; then printf '2 2 2\n'; else printf '2 1 1\n'; fi
    ;;
  *' get deployment '*) printf 'apiVersion: apps/v1\nkind: Deployment\n' ;;
  *' get validatingwebhookconfigurations.admissionregistration.k8s.io '*) printf 'validatingwebhookconfiguration.admissionregistration.k8s.io/kuberay-operator\n' ;;
  *' get mutatingwebhookconfigurations.admissionregistration.k8s.io '*) printf 'mutatingwebhookconfiguration.admissionregistration.k8s.io/kuberay-operator\n' ;;
  *' get rayjobs.ray.io,rayclusters.ray.io,rayservices.ray.io '*) printf 'apiVersion: v1\nitems: []\n' ;;
  *' get clusterqueues.kueue.x-k8s.io,resourceflavors.kueue.x-k8s.io,localqueues.kueue.x-k8s.io,workloads.kueue.x-k8s.io '*) printf 'apiVersion: v1\nitems: []\n' ;;
  *' rollout status '*) printf 'deployment successfully rolled out\n' ;;
  *' replace -k '*) printf 'customresourcedefinition.apiextensions.k8s.io replaced\n' ;;
  *) printf 'fixture\n' ;;
esac
FIXTURE

cat >"${temporary}/bin/helm" <<'FIXTURE'
#!/usr/bin/env bash
set -euo pipefail
printf 'helm %s\n' "$*" >>"${FIXTURE_LOG}"
case " $* " in
  *' get values '*) printf 'replicaCount: 2\n' ;;
  *' upgrade '*) printf 'Release upgraded\n' ;;
  *) printf 'fixture\n' ;;
esac
FIXTURE
chmod +x "${temporary}/bin/kubectl" "${temporary}/bin/helm"

fixture_env=(
  "PATH=${temporary}/bin:${PATH}"
  "FIXTURE_LOG=${fixture_log}"
  'KUBERAY_CONTEXT=fixture-context'
  'CONFIRM_KUBE_CONTEXT=fixture-context'
  'KUBERAY_NAMESPACE=kuberay-system'
  'KUBERAY_RELEASE=kuberay-operator'
)

if env "${fixture_env[@]}" CONFIRM_KUBE_CONTEXT=wrong "$preflight" >"${temporary}/out" 2>"${temporary}/err"; then
  fail 'preflight accepted an unconfirmed context'
fi
grep -Fq 'current context: fixture-context' "${temporary}/err" || fail 'preflight did not record current context'

for state in PENDING RUNNING UNKNOWN ''; do
  if env "${fixture_env[@]}" FIXTURE_RAYJOBS="tenant-a job-a ${state}\n" "$preflight" >/dev/null 2>&1; then
    fail "preflight accepted non-terminal RayJob state: ${state:-missing}"
  fi
done
env "${fixture_env[@]}" FIXTURE_RAYJOBS=$'tenant-a job-a SUCCEEDED\ntenant-a job-b FAILED\ntenant-a job-c STOPPED\n' "$preflight" >/dev/null

if env "${fixture_env[@]}" FIXTURE_RAYCLUSTERS=$'tenant-a cluster-a READY\n' "$preflight" >/dev/null 2>&1; then
  fail 'preflight accepted an active RayCluster'
fi
if env "${fixture_env[@]}" FIXTURE_DEBUG_CLUSTERS=$'tenant-a debug-a READY\n' "$preflight" >/dev/null 2>&1; then
  fail 'preflight accepted a running debug workspace'
fi
if env "${fixture_env[@]}" FIXTURE_KUEUE_HEALTHY=0 "$preflight" >/dev/null 2>&1; then
  fail 'preflight accepted unhealthy Kueue'
fi
if env "${fixture_env[@]}" FIXTURE_OPERATOR_HEALTHY=0 "$preflight" >/dev/null 2>&1; then
  fail 'preflight accepted fewer than two ready operator replicas'
fi
if env "${fixture_env[@]}" FIXTURE_CRD_VERSIONS='v1 true false' "$preflight" >/dev/null 2>&1; then
  fail 'preflight accepted a CRD without v1 served/storage'
fi

if env "${fixture_env[@]}" "$backup" relative/path >/dev/null 2>&1; then
  fail 'backup accepted a relative target path'
fi
backup_target="${temporary}/backup-output"
env "${fixture_env[@]}" "$backup" "$backup_target" >/dev/null
for artifact in context.txt crds.yaml operator-deployment.yaml operator-helm-values.yaml ray-resources.yaml kueue-resources.yaml; do
  [[ -s "${backup_target}/${artifact}" ]] || fail "backup artifact missing: ${artifact}"
done

: >"${fixture_log}"
if env "${fixture_env[@]}" "$upgrade" >/dev/null 2>&1; then
  fail 'upgrade ran without CONFIRM_KUBERAY_UPGRADE=1'
fi
if grep -Eq 'kubectl .*replace -k|helm upgrade' "${fixture_log}"; then
  fail 'upgrade mutated the fixture cluster without explicit confirmation'
fi

: >"${fixture_log}"
env "${fixture_env[@]}" CONFIRM_KUBERAY_UPGRADE=1 "$upgrade" >/dev/null
replace_line="$(grep -n 'kubectl replace -k' "${fixture_log}" | head -1 | cut -d: -f1)"
helm_line="$(grep -n 'helm upgrade kuberay-operator' "${fixture_log}" | head -1 | cut -d: -f1)"
[[ -n "$replace_line" && -n "$helm_line" && "$replace_line" -lt "$helm_line" ]] || fail 'CRDs were not upgraded before the operator'
grep -Fq -- '--version 1.6.2' "${fixture_log}" || fail 'operator upgrade is not pinned to 1.6.2'
grep -Fq -- '/apis/ray.io/v1' "${fixture_log}" || fail 'post-upgrade verification did not run'

echo 'KubeRay 1.6.2 guarded-upgrade contract verified'
