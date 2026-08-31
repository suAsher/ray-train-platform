#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly CHART="${ROOT_DIR}/helm/ray-train-platform"
readonly PROFILE="${ROOT_DIR}/deploy/profiles/vke-production-streaming.yaml"

fail() {
  echo "production streaming profile contract: $*" >&2
  exit 1
}

profile_section() {
  local section="$1"

  awk -v header="${section}:" '
    $0 == header { in_section=1 }
    in_section && /^[^[:space:]#]/ && $0 != header { exit }
    in_section { print }
  ' "$PROFILE"
}

require_block() {
  local actual="$1"
  local expected="$2"
  local message="$3"

  grep -Fq -- "$expected" <<<"$actual" || fail "$message"
}

[[ -f "$PROFILE" ]] || fail "missing ${PROFILE#"${ROOT_DIR}/"}"

publisher="$(profile_section datasetPublisher)"
ray_train="$(profile_section rayTrain)"
monitoring="$(profile_section monitoring)"

for required_publisher_value in \
  '  enabled: true' \
  '  sourceBucket: shanghai-data-transfer' \
  '  targetBucket: shanghai-data-transfer' \
  '  endpoint: https://tos-cn-shanghai.ivolces.com' \
  '  region: cn-shanghai' \
  '  sourceIndexName: .raytrain/trusted-index-v2.pkl' \
  '    irsaRoleTRN: ""' \
  '  credentials:' \
  '    existingSecret: tos-credentials' \
  '    repository: ray-dataset-publisher' \
  '    tag: tos-parallel-r7' \
  '    digest: sha256:e226a91015ebc4700776e3632cc6e774dfdd0577c139826951f67296585412bf' \
  '  priorityValue: -1000' \
  '  workingDirectory: /data/output'; do
  require_block "$publisher" "$required_publisher_value" \
    "publisher --reuse-values profile is missing: ${required_publisher_value}"
done
require_block "$publisher" '  imagePullPolicy: ""' \
  'publisher imagePullPolicy must be explicit under --reuse-values'
require_block "$publisher" '  priorityClassName: ""' \
  'publisher priorityClassName must be explicit under --reuse-values'
require_block "$publisher" '  kueue:
    resourceFlavorName: ""
    clusterQueueName: ""
    localQueueName: ""
    cpuQuota: 16
    memoryQuota: 64Gi
    resourceFlavorNodeLabels: {}' \
  'publisher generated Kueue names must be explicit under --reuse-values'
require_block "$publisher" '  tolerations: []' \
  'publisher tolerations must be explicit under --reuse-values'
require_block "$publisher" '  resources:
    requests:
      cpu: 16
      memory: 64Gi
    limits:
      cpu: 64
      memory: 256Gi' \
  'publisher CPU-only resources must be complete'
require_block "$publisher" '  job:
    backoffLimit: 3
    activeDeadlineSeconds: 604800
    ttlSecondsAfterFinished: 86400' \
  'publisher Job lifecycle must be complete'
require_block "$publisher" '  kubernetesClient:
    maxAttempts: 3
    initialRetryBackoffSeconds: 1
    maximumRetryBackoffSeconds: 30' \
  'publisher Kubernetes retry policy must be complete'
require_block "$publisher" '  manager:
    pollIntervalSeconds: 10' \
  'publisher manager polling must be explicit under --reuse-values'
require_block "$publisher" '  preferredNodeSelector:
    platform.wellspiking.ai/gpu-pool: production' \
  'publisher soft placement preference must be explicit under --reuse-values'
require_block "$publisher" '  nodeSelector: {}' \
  'publisher hard node selector must be explicitly empty for GPU-node fallback'

require_block "$ray_train" '  managedEnabled: true' \
  'managed Ray Train must be enabled'
require_block "$ray_train" '  managedTenants: []' \
  'managed Ray Train must include current and future teams'
require_block "$ray_train" '  canaryEnabled: true' \
  'Ray 2.58 canary must be enabled'
require_block "$ray_train" '  canaryTenants: []' \
  'Ray 2.58 canary must include current and future teams'
require_block "$ray_train" '  canaryVersion: "2.58.0"' \
  'Ray 2.58 canary version must remain pinned'
require_block "$ray_train" 'harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion-ray258@sha256:' \
  'Ray 2.58 canary image must use an immutable Harbor digest'

require_block "$monitoring" '  podMonitor:
    enabled: false' \
  'the existing cluster-wide PodMonitor must not be duplicated'
require_block "$monitoring" '  prometheusRule:
    enabled: true' \
  'streaming dataset alert rules must be enabled'

unexpected_top_level="$(awk '
  /^[[:alpha:]][[:alnum:]]*:$/ {
    key=$1
    sub(/:$/, "", key)
    if (key != "backend" && key != "frontend" && key != "spkRayjobRelease" &&
        key != "rayTrain" && key != "datasetPublisher" && key != "monitoring") print key
  }
' "$PROFILE")"
[[ -z "$unexpected_top_level" ]] || \
  fail "profile changes an unexpected top-level value: ${unexpected_top_level}"

if grep -Eiq '(^|[[:space:]])(access.?key|secret.?key|ak|sk|password|token):|kind:[[:space:]]*Secret|secretKeyRef|envFrom' "$PROFILE"; then
  fail 'profile must not contain or reference static credentials'
fi
if grep -Eiq 'nvidia\.com/gpu|type:[[:space:]]*(NodePort|LoadBalancer)|kind:[[:space:]]*Ray(Job|Cluster)' "$PROFILE"; then
  fail 'profile must not request GPUs, expose NodePorts, or manage tenant Ray resources'
fi

command -v helm >/dev/null 2>&1 || fail 'Helm is required for rendered production verification'
command -v python3 >/dev/null 2>&1 || fail 'Python 3 is required for manifest change verification'
baseline="$(mktemp)"
rendered="$(mktemp)"
trap 'rm -f "$baseline" "$rendered"' EXIT

helm template ray-platform "$CHART" \
  --namespace ray-train-platform \
  --values "${ROOT_DIR}/deploy/profiles/vke-cpu-ha.yaml" \
  --values "${ROOT_DIR}/deploy/profiles/vke-production-raydata-stage.yaml" >"$baseline"

helm template ray-platform "$CHART" \
  --namespace ray-train-platform \
  --values "${ROOT_DIR}/deploy/profiles/vke-cpu-ha.yaml" \
  --values "${ROOT_DIR}/deploy/profiles/vke-production-raydata-stage.yaml" \
  --values "$PROFILE" >"$rendered"

grep -Fq 'kind: PrometheusRule' "$rendered" || \
  fail 'production render must include the streaming PrometheusRule'
if grep -Eq '^kind: PodMonitor$|^[[:space:]]+type:[[:space:]]+(NodePort|LoadBalancer)$|^kind: Ray(Job|Cluster)$' "$rendered"; then
  fail 'production render must not duplicate PodMonitor, expose NodePorts, or own tenant Ray resources'
fi

python3 - "$baseline" "$rendered" <<'PY'
import hashlib
import re
import sys
from pathlib import Path


def manifests(path):
    resources = {}
    for raw in re.split(r"(?m)^---\s*$", Path(path).read_text()):
        document = raw.strip()
        if not document:
            continue
        kind_match = re.search(r"(?m)^kind:\s*([^\s#]+)", document)
        metadata_match = re.search(r"(?m)^metadata:\s*$", document)
        if not kind_match or not metadata_match:
            continue
        metadata = document[metadata_match.end():]
        name_match = re.search(r"(?m)^  name:\s*([^\s#]+)", metadata)
        namespace_match = re.search(r"(?m)^  namespace:\s*([^\s#]+)", metadata)
        if not name_match:
            raise SystemExit("rendered resource has no metadata.name")
        key = (
            kind_match.group(1).strip('"'),
            namespace_match.group(1).strip('"') if namespace_match else "",
            name_match.group(1).strip('"'),
        )
        if key in resources:
            raise SystemExit(f"duplicate rendered resource: {key}")
        canonical = "\n".join(line.rstrip() for line in document.splitlines())
        resources[key] = hashlib.sha256(canonical.encode()).hexdigest()
    return resources


baseline = manifests(sys.argv[1])
target = manifests(sys.argv[2])
removed = set(baseline) - set(target)
changed = {key for key in baseline.keys() & target.keys() if baseline[key] != target[key]}
added = set(target) - set(baseline)

expected_changed = {
    ("Deployment", "ray-train-platform", "ray-train-backend"),
    ("Deployment", "ray-train-platform", "ray-train-frontend"),
    ("Deployment", "ray-train-platform", "spk-rayjob-release"),
}
expected_added = {
    ("ClusterQueue", "", "ray-platform-ray-train-platform-dataset-publisher-clusterqueue"),
    ("ConfigMap", "ray-train-platform", "ray-platform-ray-train-platform-dataset-publisher"),
    ("LocalQueue", "ray-train-platform", "ray-platform-ray-train-platform-dataset-publisher-queue"),
    ("PriorityClass", "", "ray-platform-ray-train-platform-dataset-publisher-priority"),
    ("PrometheusRule", "monitoring", "ray-platform-ray-train-platform-dataset"),
    ("ResourceFlavor", "", "ray-platform-ray-train-platform-dataset-publisher-flavor"),
    ("Role", "ray-train-platform", "ray-platform-ray-train-platform-dataset-publisher"),
    ("RoleBinding", "ray-train-platform", "ray-platform-ray-train-platform-dataset-publisher"),
    ("ServiceAccount", "ray-train-platform", "ray-platform-ray-train-platform-dataset-publisher"),
}

problems = []
if removed:
    problems.append(f"removes existing resources: {sorted(removed)}")
if changed != expected_changed:
    problems.append(
        f"modified resources differ from deployment allowlist: expected={sorted(expected_changed)} actual={sorted(changed)}"
    )
if added != expected_added:
    problems.append(
        f"added resources differ from publisher/monitoring allowlist: expected={sorted(expected_added)} actual={sorted(added)}"
    )
if problems:
    raise SystemExit("; ".join(problems))
PY

echo 'Production streaming profile contract verified'
