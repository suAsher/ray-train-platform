#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly ops_dir="${root_dir}/ops/kuberay"
readonly preflight="${ops_dir}/preflight-upgrade.sh"
readonly backup="${ops_dir}/backup.sh"
readonly upgrade="${ops_dir}/upgrade-1.6.2.sh"
readonly verify="${ops_dir}/verify.sh"
readonly chart_checksum="${ops_dir}/checksums/kuberay-operator-1.6.2.sha256"

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

require_file "$chart_checksum"
require_in "$chart_checksum" '660e709573ea49455fea0b34b40a38afc217b8354bcf0f17aab2a3249d3c1b5f  kuberay-operator-1.6.2.tgz'

for expected in \
  'kubectl get rayjobs.ray.io --all-namespaces' \
  'kubectl get rayclusters.ray.io --all-namespaces' \
  'ray.io/dev-workspace=true' \
  'SUCCEEDED|FAILED|STOPPED' \
  'kueue-controller-manager' \
  'clusterqueues.kueue.x-k8s.io' \
  'workloads.kueue.x-k8s.io' \
  'Finished' \
  'ClusterQueue is not Active' \
  'operator replicas are not ready' \
  'kubectl auth can-i'; do
  require_in "$preflight" "$expected"
done

for expected in \
  'backup target must be an absolute safe parent path' \
  'operator-deployment.yaml' \
  'operator-helm-values.yaml' \
  'operator-helm-status.yaml' \
  'operator-helm-history.yaml' \
  'operator-helm-manifest.yaml' \
  'operator-chart-metadata.json' \
  'operator-pod-images.txt' \
  'checksums.sha256' \
  'COMPLETE' \
  'ray-resources.yaml' \
  'kueue-resources.yaml' \
  'ray_resource_types' \
  'kubectl get "$ray_resource_types" --all-namespaces'; do
  require_in "$backup" "$expected"
done
require_in "$backup" '--kube-context'

for expected in \
  "KUBERAY_TARGET_VERSION='1.6.2'" \
  'https://github.com/ray-project/kuberay-helm/releases/download/kuberay-operator-1.6.2/kuberay-operator-1.6.2.tgz' \
  'raw.githubusercontent.com/ray-project/kuberay/598eb66' \
  'EXPECTED_KUBERAY_CRD_SHA256' \
  'EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST' \
  'KUBERAY_BACKUP_PARENT' \
  'ray-train-backend' \
  'stopPolicy' \
  'Hold' \
  'CONFIRM_KUBERAY_UPGRADE' \
  'kubectl replace -k' \
  'helm upgrade' \
  '--skip-crds' \
  '--reuse-values' \
  'image.repository' \
  'image.tag' \
  'replicas' \
  'preflight-upgrade.sh' \
  'verify.sh'; do
  require_in "$upgrade" "$expected"
done
require_in "$upgrade" '--kube-context'
require_in "$upgrade" '.status.replicas'
require_in "$upgrade" 'KUBERAY_CONTEXT="$target_context" "${script_dir}/preflight-upgrade.sh"'
require_in "$upgrade" '"${script_dir}/verify.sh"'
for forbidden in 'kubectl delete' 'kubectl patch rayjobs' 'kubectl set image' 'ray runtime image'; do
  reject_in "$upgrade" "$forbidden"
done

for expected in \
  'served/storage' \
  'kubectl rollout status' \
  'readyReplicas' \
  'kuberay-operator-1.6.2' \
  'quay.io/kuberay/operator:v1.6.2' \
  'EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST' \
  '/apis/ray.io/v1' \
  'webhook' \
  'rayjobs.ray.io,rayclusters.ray.io,rayservices.ray.io,raycronjobs.ray.io' \
  'clusterqueues.kueue.x-k8s.io,resourceflavors.kueue.x-k8s.io,localqueues.kueue.x-k8s.io,workloads.kueue.x-k8s.io'; do
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
  *' get rayjobs.ray.io --all-namespaces '*)
    if [[ "$(cat "${FIXTURE_STATE_DIR}/backend-replicas")" == 0 && "$(cat "${FIXTURE_STATE_DIR}/queue-policy")" == Hold ]]; then printf '%b' "${FIXTURE_POST_GATE_RAYJOBS:-${FIXTURE_RAYJOBS:-}}"; else printf '%b' "${FIXTURE_RAYJOBS:-}"; fi
    ;;
  *' get rayclusters.ray.io --all-namespaces '*'-l ray.io/dev-workspace=true'*) printf '%b' "${FIXTURE_DEBUG_CLUSTERS:-}" ;;
  *' get rayclusters.ray.io --all-namespaces '*)
    if [[ "$(cat "${FIXTURE_STATE_DIR}/backend-replicas")" == 0 && "$(cat "${FIXTURE_STATE_DIR}/queue-policy")" == Hold ]]; then printf '%b' "${FIXTURE_POST_GATE_RAYCLUSTERS:-${FIXTURE_RAYCLUSTERS:-}}"; else printf '%b' "${FIXTURE_RAYCLUSTERS:-}"; fi
    ;;
  *' get workloads.kueue.x-k8s.io --all-namespaces '*)
    if [[ "$(cat "${FIXTURE_STATE_DIR}/backend-replicas")" == 0 && "$(cat "${FIXTURE_STATE_DIR}/queue-policy")" == Hold ]]; then
      printf '%b' "${FIXTURE_POST_GATE_WORKLOADS:-${FIXTURE_WORKLOADS:-}}"
    else
      printf '%b' "${FIXTURE_WORKLOADS:-}"
    fi
    ;;
  *' get crd --context '*'.spec.group=="ray.io"'*) printf 'rayclusters.ray.io\nrayjobs.ray.io\nrayservices.ray.io\nraycronjobs.ray.io\n' ;;
  *' get crd rayjobs.ray.io '*|*' get crd rayclusters.ray.io '*|*' get crd rayservices.ray.io '*|*' get crd raycronjobs.ray.io '*)
    if [[ "${command_line}" == *'jsonpath='* ]]; then
      printf '%b\n' "${FIXTURE_CRD_VERSIONS:-v1 true true}"
    else
      printf 'apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\n'
    fi
    ;;
  *' get crd clusterqueues.kueue.x-k8s.io '*|*' get crd localqueues.kueue.x-k8s.io '*) printf 'crd.fixture\n' ;;
  *' get clusterqueues.kueue.x-k8s.io --all-namespaces '*'{"\t"}'*) printf 'queue-a\t%s\n' "$(cat "${FIXTURE_STATE_DIR}/queue-policy")" ;;
  *' get clusterqueues.kueue.x-k8s.io --all-namespaces '*)
    policy="$(cat "${FIXTURE_STATE_DIR}/queue-policy")"
    if [[ "${FIXTURE_CLUSTERQUEUES_HEALTHY:-1}" == 1 && "$policy" != Hold ]]; then
      printf 'queue-a True %s\n' "$policy"
    else
      printf 'queue-a False %s\n' "$policy"
    fi
    ;;
  *' get deployment ray-train-backend '*'.status.readyReplicas'*)
    desired="$(cat "${FIXTURE_STATE_DIR}/backend-replicas")"
    current="$(cat "${FIXTURE_STATE_DIR}/backend-current")"
    if [[ "$desired" == 0 ]]; then printf '0 %s 0 0\n' "$current"; else printf '%s %s %s %s\n' "$desired" "$current" "$current" "$current"; fi
    ;;
  *' get deployment ray-train-backend '*'.spec.replicas'*) cat "${FIXTURE_STATE_DIR}/backend-replicas" ;;
  *' scale deployment/ray-train-backend '*--replicas=0*) printf '0\n' >"${FIXTURE_STATE_DIR}/backend-replicas"; if [[ "${FIXTURE_BACKEND_DRAIN_STUCK:-0}" == 1 ]]; then printf '1\n' >"${FIXTURE_STATE_DIR}/backend-current"; else printf '0\n' >"${FIXTURE_STATE_DIR}/backend-current"; fi ;;
  *' scale deployment/ray-train-backend '*--replicas=2*) printf '2\n' >"${FIXTURE_STATE_DIR}/backend-replicas"; printf '2\n' >"${FIXTURE_STATE_DIR}/backend-current" ;;
  *' patch clusterqueue queue-a '*Hold*) printf 'Hold\n' >"${FIXTURE_STATE_DIR}/queue-policy" ;;
  *' patch clusterqueue queue-a '*stopPolicy*null*) printf 'None\n' >"${FIXTURE_STATE_DIR}/queue-policy" ;;
  *' patch clusterqueue queue-a '*stopPolicy*None*) printf 'None\n' >"${FIXTURE_STATE_DIR}/queue-policy" ;;
  *' wait --for=condition=Active=false clusterqueue/queue-a '*) [[ "$(cat "${FIXTURE_STATE_DIR}/queue-policy")" == Hold ]] ;;
  *' wait --for=condition=Available=false deployment/ray-train-backend '*) [[ "$(cat "${FIXTURE_STATE_DIR}/backend-replicas")" == 0 ]] ;;
  *' get deployment '*'.spec.template.spec.containers'*)
    if [[ -f "${FIXTURE_STATE_DIR}/helm-upgraded" ]]; then printf 'kuberay-operator quay.io/kuberay/operator:%s\n' "${FIXTURE_OPERATOR_IMAGE_TAG:-v1.6.2}"; else printf 'kuberay-operator quay.io/kuberay/operator:v1.3.0\n'; fi
    ;;
  *' get deployment kueue-controller-manager '*)
    if [[ "${FIXTURE_KUEUE_HEALTHY:-1}" == 1 ]]; then printf '2 2 2\n'; else printf '2 1 1\n'; fi
    ;;
  *' get deployment '*'-o jsonpath='*)
    if [[ "${FIXTURE_OPERATOR_HEALTHY:-1}" == 1 ]]; then printf '2 2 2\n'; else printf '2 1 1\n'; fi
    ;;
  *' get deployment '*) printf 'apiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n      - image: quay.io/kuberay/operator:v1.3.0\n' ;;
  *' get pods '*'.status.containerStatuses'*)
    if [[ -f "${FIXTURE_STATE_DIR}/helm-upgraded" ]]; then
      printf 'kuberay-operator-0 kuberay-operator quay.io/kuberay/operator:%s quay.io/kuberay/operator@sha256:%s\n' "${FIXTURE_OPERATOR_IMAGE_TAG:-v1.6.2}" "${FIXTURE_OPERATOR_IMAGE_DIGEST:-${EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST:-}}"
      printf 'kuberay-operator-1 kuberay-operator quay.io/kuberay/operator:%s quay.io/kuberay/operator@sha256:%s\n' "${FIXTURE_OPERATOR_IMAGE_TAG:-v1.6.2}" "${FIXTURE_OPERATOR_IMAGE_DIGEST:-${EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST:-}}"
    else
      printf 'kuberay-operator-0 operator quay.io/kuberay/operator:v1.3.0 quay.io/kuberay/operator@sha256:%064d\n' 0
    fi
    ;;
  *' get validatingwebhookconfigurations.admissionregistration.k8s.io '*) printf 'validatingwebhookconfiguration.admissionregistration.k8s.io/kuberay-operator\n' ;;
  *' get mutatingwebhookconfigurations.admissionregistration.k8s.io '*) printf 'mutatingwebhookconfiguration.admissionregistration.k8s.io/kuberay-operator\n' ;;
  *' get rayjobs.ray.io,rayclusters.ray.io,rayservices.ray.io,raycronjobs.ray.io '*)
    [[ "${FIXTURE_RESOURCES_READABLE:-1}" == 1 ]] || exit 1
    printf 'apiVersion: v1\nitems: []\n'
    ;;
  *' get clusterqueues.kueue.x-k8s.io,resourceflavors.kueue.x-k8s.io,localqueues.kueue.x-k8s.io,workloads.kueue.x-k8s.io '*)
    [[ "${FIXTURE_RESOURCES_READABLE:-1}" == 1 ]] || exit 1
    printf 'apiVersion: v1\nitems: []\n'
    ;;
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
  *' get values '*) printf 'image:\n  repository: quay.io/kuberay/operator\n  tag: v1.3.0\nreplicas: 2\n' ;;
  *' status '*)
    [[ "${FIXTURE_BACKUP_FAIL:-0}" == 0 ]] || exit 1
    printf 'name: kuberay-operator\nchart: kuberay-operator-1.3.0\n'
    ;;
  *' history '*) printf 'revision: 1\nchart: kuberay-operator-1.3.0\n' ;;
  *' get manifest '*) printf 'apiVersion: apps/v1\nkind: Deployment\n' ;;
  *' list '*) printf '[{"name":"kuberay-operator","chart":"kuberay-operator-%s"}]\n' "${FIXTURE_HELM_CHART_VERSION:-1.6.2}" ;;
  *' show chart '*) printf 'name: kuberay-operator\nversion: 1.6.2\n' ;;
  *' upgrade '*) touch "${FIXTURE_STATE_DIR}/helm-upgraded"; printf 'Release upgraded\n' ;;
  *) printf 'fixture\n' ;;
esac
FIXTURE

cat >"${temporary}/bin/curl" <<'FIXTURE'
#!/usr/bin/env bash
set -euo pipefail
printf 'curl %s\n' "$*" >>"${FIXTURE_LOG}"
output=''
url=''
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o|--output) output="$2"; shift 2 ;;
    http*) url="$1"; shift ;;
    *) shift ;;
  esac
done
[[ -n "$output" && -n "$url" ]] || exit 2
case "$url" in
  https://github.com/ray-project/kuberay-helm/releases/download/kuberay-operator-1.6.2/kuberay-operator-1.6.2.tgz)
    printf 'fixture kuberay operator chart 1.6.2\n' >"$output"
    ;;
  https://raw.githubusercontent.com/ray-project/kuberay/598eb66/*)
    crd_file="${url##*/}"
    crd_name="${crd_file#ray.io_}"
    crd_name="${crd_name%.yaml}.ray.io"
    printf 'apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: %s\n' "$crd_name" >"$output"
    ;;
  *) exit 22 ;;
esac
FIXTURE

cat >"${temporary}/bin/shasum" <<'FIXTURE'
#!/usr/bin/env bash
set -euo pipefail
file="${!#}"
case "$(basename -- "$file")" in
  kuberay-operator-1.6.2.tgz)
    if [[ "${FIXTURE_CHART_CHECKSUM_MISMATCH:-0}" == 1 ]]; then digest="$(printf '%064d' 0)"; else digest='660e709573ea49455fea0b34b40a38afc217b8354bcf0f17aab2a3249d3c1b5f'; fi
    printf '%s  %s\n' "$digest" "$file"
    ;;
  crds.aggregate)
    if [[ "${FIXTURE_CRD_CHECKSUM_MISMATCH:-0}" == 1 ]]; then digest="$(printf '%064d' 0)"; else digest="${EXPECTED_KUBERAY_CRD_SHA256}"; fi
    printf '%s  %s\n' "$digest" "$file"
    ;;
  *) /usr/bin/shasum "$@" ;;
esac
FIXTURE

cat >"${temporary}/bin/backup-fail" <<'FIXTURE'
#!/usr/bin/env bash
exit 1
FIXTURE
cat >"${temporary}/bin/backup-incomplete" <<'FIXTURE'
#!/usr/bin/env bash
set -euo pipefail
target="$1/incomplete-backup"
mkdir -p "$target"
printf '%s\n' "$target"
FIXTURE

cat >"${temporary}/bin/backup-duplicate" <<'FIXTURE'
#!/usr/bin/env bash
set -euo pipefail
target="$1/duplicate-backup"
mkdir -p "$target"
printf 'fixture\n' >"$target/context.txt"
digest="$(/usr/bin/shasum -a 256 "$target/context.txt")"
digest="${digest%%[[:space:]]*}"
for _ in {1..11}; do printf '%s  context.txt\n' "$digest"; done >"$target/checksums.sha256"
printf 'COMPLETE\n' >"$target/COMPLETE"
printf '%s\n' "$target"
FIXTURE

chmod +x "${temporary}/bin/kubectl" "${temporary}/bin/helm" "${temporary}/bin/curl" "${temporary}/bin/shasum" "${temporary}/bin/backup-fail" "${temporary}/bin/backup-incomplete" "${temporary}/bin/backup-duplicate"
mkdir -p "${temporary}/state" "${temporary}/backups"
printf '2\n' >"${temporary}/state/backend-replicas"
printf '2\n' >"${temporary}/state/backend-current"
printf 'None\n' >"${temporary}/state/queue-policy"

fixture_env=(
  "PATH=${temporary}/bin:${PATH}"
  "FIXTURE_LOG=${fixture_log}"
  "FIXTURE_STATE_DIR=${temporary}/state"
  "KUBERAY_BACKUP_PARENT=${temporary}/backups"
  'CURL_BIN=curl'
  'SHA256_BIN=shasum'
  'KUBERAY_CONTEXT=fixture-context'
  'CONFIRM_KUBE_CONTEXT=fixture-context'
  'KUBERAY_NAMESPACE=kuberay-system'
  'KUBERAY_RELEASE=kuberay-operator'
  'EXPECTED_KUBERAY_CRD_SHA256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
  'CONFIRM_KUBERAY_CRD_SHA256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
  'EXPECTED_KUBERAY_OPERATOR_IMAGE_DIGEST=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
  'CONFIRM_KUBERAY_OPERATOR_IMAGE_DIGEST=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
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
if env "${fixture_env[@]}" FIXTURE_CLUSTERQUEUES_HEALTHY=0 "$preflight" >/dev/null 2>&1; then
  fail 'preflight accepted an inactive ClusterQueue'
fi
if env "${fixture_env[@]}" FIXTURE_OPERATOR_HEALTHY=0 "$preflight" >/dev/null 2>&1; then
  fail 'preflight accepted fewer than two ready operator replicas'
fi
if env "${fixture_env[@]}" FIXTURE_CRD_VERSIONS='v1 true false' "$preflight" >/dev/null 2>&1; then
  fail 'preflight accepted a CRD without v1 served/storage'
fi
if env "${fixture_env[@]}" FIXTURE_WORKLOADS=$'tenant-a workload-a False\n' "$preflight" >/dev/null 2>&1; then
  fail 'preflight accepted a non-terminal Kueue Workload'
fi
env "${fixture_env[@]}" FIXTURE_WORKLOADS=$'tenant-a workload-a True\n' "$preflight" >/dev/null
touch "${temporary}/state/helm-upgraded"
if env "${fixture_env[@]}" FIXTURE_RESOURCES_READABLE=0 "$verify" >/dev/null 2>&1; then
  fail 'verification accepted unreadable existing resources'
fi
rm -f "${temporary}/state/helm-upgraded"

if env "${fixture_env[@]}" "$backup" relative/path >/dev/null 2>&1; then
  fail 'backup accepted a relative target path'
fi
if env "${fixture_env[@]}" "$backup" "${temporary}/unsafe target" >/dev/null 2>&1; then
  fail 'backup accepted an unsafe target name'
fi
backup_target="$(env "${fixture_env[@]}" "$backup" "${temporary}/backups")"
for artifact in context.txt crds.yaml operator-deployment.yaml operator-helm-values.yaml operator-helm-status.yaml operator-helm-history.yaml operator-helm-manifest.yaml operator-chart-metadata.json operator-pod-images.txt ray-resources.yaml kueue-resources.yaml checksums.sha256 COMPLETE; do
  [[ -s "${backup_target}/${artifact}" ]] || fail "backup artifact missing: ${artifact}"
done
[[ "$(cat "${backup_target}/COMPLETE")" == COMPLETE ]] || fail 'backup completion marker is invalid'
grep -Fq 'get rayclusters.ray.io,rayjobs.ray.io,rayservices.ray.io,raycronjobs.ray.io --all-namespaces' "$fixture_log" || fail 'backup did not export every installed ray.io resource type'

reset_fixture_state() {
  printf '2\n' >"${temporary}/state/backend-replicas"
  printf '2\n' >"${temporary}/state/backend-current"
  printf 'None\n' >"${temporary}/state/queue-policy"
  rm -f "${temporary}/state/helm-upgraded"
  : >"${fixture_log}"
}

assert_no_cluster_writes() {
  if grep -Eq 'kubectl (scale|patch|replace)|helm upgrade' "${fixture_log}"; then
    fail "$1"
  fi
}

reset_fixture_state
if env "${fixture_env[@]}" "$upgrade" >/dev/null 2>&1; then
  fail 'upgrade ran without CONFIRM_KUBERAY_UPGRADE=1'
fi
assert_no_cluster_writes 'upgrade mutated the fixture cluster without explicit confirmation'

reset_fixture_state
if env "${fixture_env[@]}" CONFIRM_KUBERAY_UPGRADE=1 FIXTURE_CHART_CHECKSUM_MISMATCH=1 "$upgrade" >/dev/null 2>&1; then
  fail 'upgrade accepted a chart checksum mismatch'
fi
assert_no_cluster_writes 'chart checksum mismatch caused a cluster write'

reset_fixture_state
if env "${fixture_env[@]}" CONFIRM_KUBERAY_UPGRADE=1 FIXTURE_CRD_CHECKSUM_MISMATCH=1 "$upgrade" >/dev/null 2>&1; then
  fail 'upgrade accepted a CRD aggregate checksum mismatch'
fi
assert_no_cluster_writes 'CRD checksum mismatch caused a cluster write'

reset_fixture_state
if env "${fixture_env[@]}" CONFIRM_KUBERAY_UPGRADE=1 KUBERAY_CHART_URL='https://evil.example/kuberay-operator-1.6.2.tgz' "$upgrade" >/dev/null 2>&1; then
  fail 'upgrade accepted a non-official chart host'
fi
assert_no_cluster_writes 'non-official chart URL caused a cluster write'

reset_fixture_state
if env "${fixture_env[@]}" CONFIRM_KUBERAY_UPGRADE=1 KUBERAY_BACKUP_BIN=backup-fail "$upgrade" >/dev/null 2>&1; then
  fail 'upgrade continued after backup failure'
fi
assert_no_cluster_writes 'backup failure caused a cluster write'

reset_fixture_state
if env "${fixture_env[@]}" CONFIRM_KUBERAY_UPGRADE=1 KUBERAY_BACKUP_BIN=backup-incomplete "$upgrade" >/dev/null 2>&1; then
  fail 'upgrade accepted an incomplete backup'
fi
assert_no_cluster_writes 'incomplete backup caused a cluster write'

reset_fixture_state
if env "${fixture_env[@]}" CONFIRM_KUBERAY_UPGRADE=1 KUBERAY_BACKUP_BIN=backup-duplicate "$upgrade" >/dev/null 2>&1; then
  fail 'upgrade accepted a backup checksum manifest with duplicate files'
fi
assert_no_cluster_writes 'duplicate backup checksum entries caused a cluster write'

reset_fixture_state
if env "${fixture_env[@]}" CONFIRM_KUBERAY_UPGRADE=1 FIXTURE_BACKEND_DRAIN_STUCK=1 MAINTENANCE_WAIT_ATTEMPTS=1 MAINTENANCE_WAIT_INTERVAL_SECONDS=0 "$upgrade" >/dev/null 2>&1; then
  fail 'upgrade accepted a backend Pod that remained alive after scale-to-zero'
fi
if grep -Eq 'kubectl patch clusterqueue|kubectl replace -k|helm upgrade' "${fixture_log}"; then
  fail 'upgrade advanced beyond a backend maintenance gate that had not drained'
fi
[[ "$(cat "${temporary}/state/backend-replicas")" == 2 && "$(cat "${temporary}/state/backend-current")" == 2 ]] || fail 'backend was not restored after drain timeout'

reset_fixture_state
if env "${fixture_env[@]}" CONFIRM_KUBERAY_UPGRADE=1 FIXTURE_POST_GATE_WORKLOADS=$'tenant-a race-workload False\n' "$upgrade" >/dev/null 2>&1; then
  fail 'upgrade ignored a Kueue Workload created before the maintenance gate closed'
fi
if grep -Eq 'kubectl replace -k|helm upgrade' "${fixture_log}"; then
  fail 'post-gate workload was detected only after upgrade mutation'
fi
[[ "$(cat "${temporary}/state/backend-replicas")" == 2 ]] || fail 'backend replicas were not restored after a gated preflight failure'
[[ "$(cat "${temporary}/state/queue-policy")" == None ]] || fail 'ClusterQueue stopPolicy was not restored after a gated preflight failure'

reset_fixture_state
if env "${fixture_env[@]}" CONFIRM_KUBERAY_UPGRADE=1 FIXTURE_POST_GATE_RAYJOBS=$'tenant-a race-job RUNNING\n' "$upgrade" >/dev/null 2>&1; then
  fail 'upgrade ignored a RayJob created before the maintenance gate closed'
fi
if grep -Eq 'kubectl replace -k|helm upgrade' "${fixture_log}"; then fail 'post-gate RayJob caused an upgrade mutation'; fi

reset_fixture_state
if env "${fixture_env[@]}" CONFIRM_KUBERAY_UPGRADE=1 FIXTURE_POST_GATE_RAYCLUSTERS=$'tenant-a race-cluster READY\n' "$upgrade" >/dev/null 2>&1; then
  fail 'upgrade ignored a RayCluster created before the maintenance gate closed'
fi
if grep -Eq 'kubectl replace -k|helm upgrade' "${fixture_log}"; then fail 'post-gate RayCluster caused an upgrade mutation'; fi

touch "${temporary}/state/helm-upgraded"
if env "${fixture_env[@]}" FIXTURE_HELM_CHART_VERSION=1.3.0 "$verify" >/dev/null 2>&1; then
  fail 'verification accepted an old Helm chart version'
fi
if env "${fixture_env[@]}" FIXTURE_OPERATOR_IMAGE_TAG=v1.3.0 "$verify" >/dev/null 2>&1; then
  fail 'verification accepted an old operator image tag'
fi
if env "${fixture_env[@]}" FIXTURE_OPERATOR_IMAGE_DIGEST=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc "$verify" >/dev/null 2>&1; then
  fail 'verification accepted an unexpected running image digest'
fi

reset_fixture_state
upgrade_output="$(env "${fixture_env[@]}" CONFIRM_KUBERAY_UPGRADE=1 "$upgrade" 2>&1)"
grep -Fq 'backup path:' <<<"$upgrade_output" || fail 'upgrade did not report its verified backup path'
replace_line="$(grep -n 'kubectl replace -k' "${fixture_log}" | head -1 | cut -d: -f1)"
helm_line="$(grep -n 'helm upgrade kuberay-operator' "${fixture_log}" | head -1 | cut -d: -f1)"
[[ -n "$replace_line" && -n "$helm_line" && "$replace_line" -lt "$helm_line" ]] || fail 'CRDs were not upgraded before the operator'
grep -Fq -- '--set-string image.repository=quay.io/kuberay/operator' "${fixture_log}" || fail 'operator repository was not explicitly overridden'
grep -Fq -- '--set-string image.tag=v1.6.2' "${fixture_log}" || fail 'old Helm values could retain the v1.3 operator image tag'
grep -Fq -- '--set replicas=2' "${fixture_log}" || fail 'operator replica count was not explicitly overridden'
grep -Fq -- '/apis/ray.io/v1' "${fixture_log}" || fail "post-upgrade verification did not run: ${upgrade_output}"
[[ "$(cat "${temporary}/state/backend-replicas")" == 2 ]] || fail 'backend replicas were not restored after success'
[[ "$(cat "${temporary}/state/queue-policy")" == None ]] || fail 'ClusterQueue stopPolicy was not restored after success'
while read -r mutation; do
  [[ "$mutation" == *'--context fixture-context'* || "$mutation" == helm\ upgrade*'--kube-context fixture-context'* ]] || fail "mutation omitted explicit context: $mutation"
done < <(grep -E '^(kubectl (scale|patch|replace)|helm upgrade)' "${fixture_log}")

echo 'KubeRay 1.6.2 guarded-upgrade contract verified'
