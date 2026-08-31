#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly CHART="${ROOT_DIR}/helm/ray-train-platform"
readonly VALUES="${CHART}/values.yaml"
readonly PROFILE="${ROOT_DIR}/deploy/profiles/test.yaml"
readonly PRODUCTION_STREAMING_PROFILE="${ROOT_DIR}/deploy/profiles/vke-production-streaming.yaml"
readonly BACKEND_TEMPLATE="${CHART}/templates/backend-deployment.yaml"
readonly SERVICE_ACCOUNT_TEMPLATE="${CHART}/templates/dataset-publisher-serviceaccount.yaml"
readonly RBAC_TEMPLATE="${CHART}/templates/dataset-publisher-rbac.yaml"
readonly CONFIG_TEMPLATE="${CHART}/templates/dataset-publisher-config.yaml"
readonly HELPERS_TEMPLATE="${CHART}/templates/dataset-publisher-helpers.yaml"
readonly SCHEDULING_TEMPLATE="${CHART}/templates/dataset-publisher-scheduling.yaml"

fail() {
  echo "dataset publisher render contract: $*" >&2
  exit 1
}

require_file() {
  local file="$1"

  [[ -f "$file" ]] || fail "missing ${file#"${ROOT_DIR}/"}"
}

require_literal() {
  local file="$1"
  local expected="$2"
  local message="$3"

  grep -Fq -- "$expected" "$file" || fail "$message"
}

reject_pattern() {
  local file="$1"
  local pattern="$2"
  local message="$3"

  if grep -Eiq -- "$pattern" "$file"; then
    fail "$message"
  fi
}

publisher_values() {
  awk '
    /^datasetPublisher:$/ { in_publisher=1 }
    in_publisher && /^[^[:space:]#]/ && $0 != "datasetPublisher:" { exit }
    in_publisher { print }
  ' "$VALUES"
}

production_publisher_values() {
  awk '
    /^datasetPublisher:$/ { in_publisher=1 }
    in_publisher && /^[^[:space:]#]/ && $0 != "datasetPublisher:" { exit }
    in_publisher { print }
  ' "$PRODUCTION_STREAMING_PROFILE"
}

assert_source_contract() {
  local template
  local rbac_resource_rule_count
  local rbac_service_account_subject_count
  local production_values_block
  local values_block

  require_file "$VALUES"
  require_file "$BACKEND_TEMPLATE"
  require_file "$SERVICE_ACCOUNT_TEMPLATE"
  require_file "$RBAC_TEMPLATE"
  require_file "$CONFIG_TEMPLATE"
  require_file "$HELPERS_TEMPLATE"
  require_file "$SCHEDULING_TEMPLATE"
  require_file "$PRODUCTION_STREAMING_PROFILE"

  require_literal "$VALUES" '  datasetVersioningEnabled: false' \
    'DATASET_VERSIONING_ENABLED Helm value must default to false'
  require_literal "$BACKEND_TEMPLATE" '            - name: DATASET_VERSIONING_ENABLED' \
    'backend must expose DATASET_VERSIONING_ENABLED'
  require_literal "$BACKEND_TEMPLATE" '              value: {{ default false .Values.backend.datasetVersioningEnabled | quote }}' \
    'DATASET_VERSIONING_ENABLED must use the same default-off Helm gate'
  require_literal "$BACKEND_TEMPLATE" 'checksum/dataset-publisher-config:' \
    'backend pods must roll when dataset publisher ConfigMap values change'
  require_literal "$VALUES" 'datasetPublisher:' \
    'values must provide datasetPublisher configuration'

  values_block="$(publisher_values)"
  grep -Fq '  enabled: false' <<<"$values_block" || \
    fail 'publisher rollout gate must default to false independently of backend feature flags'
  grep -Fq '  sourceBucket: ""' <<<"$values_block" || \
    fail 'publisher source bucket must support explicit deployment override'
  grep -Fq '  targetBucket: ""' <<<"$values_block" || \
    fail 'publisher target bucket must support explicit deployment override'
  grep -Fq '  sourceIndexName: .raytrain/trusted-index-v2.pkl' <<<"$values_block" || \
    fail 'publisher trusted source index must have an explicit deployment default'
  grep -Fq '    irsaRoleTRN: ""' <<<"$values_block" || \
    fail 'IRSA role TRN must default to empty'
  grep -Fq '    existingSecret: ""' <<<"$values_block" || \
    fail 'publisher credential Secret reference must default to empty'
  require_literal "$CONFIG_TEMPLATE" 'DATASET_PUBLISHER_CREDENTIAL_SECRET:' \
    'publisher ConfigMap must carry only the existing credential Secret name'
  grep -Fq '    resourceFlavorName: ""' <<<"$values_block" || \
    fail 'publisher ResourceFlavor must default to an automatic release-scoped name'
  grep -Fq '    clusterQueueName: ""' <<<"$values_block" || \
    fail 'publisher ClusterQueue must default to an automatic release-scoped name'
  grep -Fq '    localQueueName: ""' <<<"$values_block" || \
    fail 'publisher LocalQueue must default to an automatic release-scoped name'
  grep -Fq '    cpuQuota: 16' <<<"$values_block" || \
    fail 'publisher CPU ClusterQueue must have an explicit CPU quota'
  grep -Fq '    memoryQuota: 64Gi' <<<"$values_block" || \
    fail 'publisher CPU ClusterQueue must have an explicit memory quota'
  grep -Fq '    resourceFlavorNodeLabels: {}' <<<"$values_block" || \
    fail 'publisher ResourceFlavor must not hard-bind a CPU node pool by default'
  grep -Fq '  priorityClassName: ""' <<<"$values_block" || \
    fail 'publisher PriorityClass must default to an automatic release-scoped name'
  grep -Fq '  job:' <<<"$values_block" || \
    fail 'publisher values must separate Job execution settings'
  grep -Fq '    backoffLimit: 3' <<<"$values_block" || \
    fail 'publisher Job backoffLimit must have an explicit default'
  grep -Fq '    activeDeadlineSeconds: 604800' <<<"$values_block" || \
    fail 'publisher Job active deadline must have an explicit bounded default'
  grep -Fq '    ttlSecondsAfterFinished: 86400' <<<"$values_block" || \
    fail 'publisher Job TTL must have an explicit bounded default'
  grep -Fq '  kubernetesClient:' <<<"$values_block" || \
    fail 'publisher values must separate Kubernetes client retry settings'
  grep -Fq '    maxAttempts: 3' <<<"$values_block" || \
    fail 'Kubernetes client maxAttempts must have an explicit default'
  grep -Fq '    initialRetryBackoffSeconds: 1' <<<"$values_block" || \
    fail 'Kubernetes client initial retry backoff must have an explicit default'
  grep -Fq '    maximumRetryBackoffSeconds: 30' <<<"$values_block" || \
    fail 'Kubernetes client maximum retry backoff must have an explicit default'
  grep -Fq '    pollIntervalSeconds: 10' <<<"$values_block" || \
    fail 'publisher manager polling must have an explicit default'
  grep -Fq '  preferredNodeSelector: {}' <<<"$values_block" || \
    fail 'CPU-node preference must default to a portable empty selector'
  grep -Fq '  nodeSelector: {}' <<<"$values_block" || \
    fail 'publisher nodeSelector must default to empty so GPU-node CPU fallback remains possible'
  grep -Fq '  tolerations: []' <<<"$values_block" || \
    fail 'publisher tolerations must default to empty'
  if grep -Eiq 'nvidia\.com/gpu|^[[:space:]]*affinity:|^[[:space:]]{2}(backoffLimit|retryBackoffSeconds):' <<<"$values_block"; then
    fail 'publisher defaults must not request GPUs or impose hard affinity'
  fi

  # This profile is applied with Helm --reuse-values. Newly introduced chart
  # defaults are not inherited in that mode, so the publisher subtree must be
  # self-contained instead of depending on values.yaml.
  production_values_block="$(production_publisher_values)"
  grep -Fq '    existingSecret: tos-credentials' <<<"$production_values_block" || \
    fail 'production publisher must reference the existing TOS credential Secret'
  for required_block in \
    '  priorityValue: -1000' \
    '  resources:
    requests:
      cpu: 16
      memory: 64Gi
    limits:
      cpu: 64
      memory: 256Gi' \
    '  job:
    backoffLimit: 3
    activeDeadlineSeconds: 604800
    ttlSecondsAfterFinished: 86400' \
    '  kubernetesClient:
    maxAttempts: 3
    initialRetryBackoffSeconds: 1
    maximumRetryBackoffSeconds: 30' \
    '  manager:
    pollIntervalSeconds: 10' \
    '  workingDirectory: /data/output'; do
    grep -Fq "$required_block" <<<"$production_values_block" || \
      fail "--reuse-values production profile is missing a complete publisher runtime block"
  done

  for template in "$SERVICE_ACCOUNT_TEMPLATE" "$RBAC_TEMPLATE" "$CONFIG_TEMPLATE" "$SCHEDULING_TEMPLATE"; do
    require_literal "$template" '{{- if (default false .Values.datasetPublisher.enabled) }}' \
      "${template##*/} must use the publisher-specific rollout gate"
    reject_pattern "$template" 'nvidia\.com/gpu' \
      "${template##*/} must remain CPU-only"
    reject_pattern "$template" 'kind:[[:space:]]*Secret|secretKeyRef|envFrom|\.Values\.tos\.secretName' \
      "${template##*/} must not create or consume a TOS Secret"
    reject_pattern "$template" '\.Values\.kueue\.enabled|\.Values\.backend\.datasetVersioningEnabled' \
      "${template##*/} must not depend on training Kueue or the backend feature gate"
  done

  require_literal "$HELPERS_TEMPLATE" 'define "ray-train-platform.datasetPublisher.resourceName"' \
    'publisher names must share one release-scoped DNS-safe helper'
  require_literal "$HELPERS_TEMPLATE" 'sha256sum' \
    'publisher long-name truncation must retain a collision-resistant hash'
  require_literal "$HELPERS_TEMPLATE" 'define "ray-train-platform.datasetPublisher.irsaRoleTRN"' \
    'publisher IRSA role must use one shared sanitizing helper'
  require_literal "$HELPERS_TEMPLATE" 'must be a valid Volcengine IAM role TRN' \
    'publisher IRSA role helper must reject malformed values'
  require_literal "$BACKEND_TEMPLATE" '          envFrom:' \
    'backend must consume publisher non-secret configuration'
  require_literal "$BACKEND_TEMPLATE" 'name: {{ include "ray-train-platform.datasetPublisher.resourceName" (list . "") }}' \
    'backend must consume the matching release-scoped publisher ConfigMap'

  require_literal "$SERVICE_ACCOUNT_TEMPLATE" 'kind: ServiceAccount' \
    'publisher must have a dedicated ServiceAccount'
  require_literal "$SERVICE_ACCOUNT_TEMPLATE" 'automountServiceAccountToken: false' \
    'publisher workload must not receive a Kubernetes API token'
  require_literal "$SERVICE_ACCOUNT_TEMPLATE" 'vke.volcengine.com/role-trn:' \
    'publisher ServiceAccount must support the Volcengine IRSA annotation'

  require_literal "$RBAC_TEMPLATE" 'kind: Role' \
    'publisher RBAC must be namespace-scoped'
  require_literal "$RBAC_TEMPLATE" 'kind: RoleBinding' \
    'publisher RBAC must bind a namespace-scoped Role'
  require_literal "$RBAC_TEMPLATE" 'name: ray-train-platform-sa' \
    'publisher controller Role must bind the backend controller ServiceAccount'
  rbac_service_account_subject_count="$(grep -Ec '^[[:space:]]+- kind: ServiceAccount$' "$RBAC_TEMPLATE" || true)"
  [[ "$rbac_service_account_subject_count" -eq 1 ]] || \
    fail "publisher RoleBinding must contain only the backend ServiceAccount subject; found ${rbac_service_account_subject_count}"
  require_literal "$RBAC_TEMPLATE" 'apiGroups: ["batch"]' \
    'publisher RBAC must target the batch API group'
  require_literal "$RBAC_TEMPLATE" 'resources: ["jobs"]' \
    'publisher RBAC must allow the backend to reconcile Jobs'
  require_literal "$RBAC_TEMPLATE" 'verbs: ["get", "list", "watch", "create"]' \
    'publisher RBAC must contain only the required Job verbs'
  require_literal "$RBAC_TEMPLATE" 'apiGroups: [""]' \
    'publisher RBAC must target the core API group for Pod receipts'
  require_literal "$RBAC_TEMPLATE" 'resources: ["pods"]' \
    'publisher RBAC must allow the backend to observe terminal Pods'
  require_literal "$RBAC_TEMPLATE" 'verbs: ["get", "list", "watch"]' \
    'publisher RBAC must keep Pod receipt access read-only'
  rbac_resource_rule_count="$(grep -Ec '^[[:space:]]+resources:' "$RBAC_TEMPLATE" || true)"
  [[ "$rbac_resource_rule_count" -eq 2 ]] || \
    fail "publisher RBAC must contain exactly the Job and Pod resource rules; found ${rbac_resource_rule_count}"
  reject_pattern "$RBAC_TEMPLATE" 'ClusterRole|ClusterRoleBinding|resources:[[:space:]]*\["secrets"\]|"(delete|deletecollection|patch|update)"' \
    'publisher RBAC must not be cluster-wide, read Secrets, or mutate existing resources'

  require_literal "$CONFIG_TEMPLATE" 'kind: ConfigMap' \
    'publisher non-sensitive configuration must use a ConfigMap'
  for key in DATASET_PUBLISHER_IMAGE DATASET_PUBLISHER_IMAGE_PULL_POLICY \
    DATASET_PUBLISHER_SOURCE_BUCKET DATASET_PUBLISHER_TARGET_BUCKET DATASET_PUBLISHER_ENDPOINT \
    DATASET_PUBLISHER_REGION DATASET_PUBLISHER_SERVICE_ACCOUNT DATASET_PUBLISHER_IRSA_ROLE_TRN \
    DATASET_PUBLISHER_QUEUE_NAME \
    DATASET_PUBLISHER_PRIORITY_CLASS_NAME DATASET_PUBLISHER_SOURCE_INDEX_NAME \
    DATASET_PUBLISHER_CPU_REQUEST DATASET_PUBLISHER_CPU_LIMIT DATASET_PUBLISHER_MEMORY_REQUEST \
    DATASET_PUBLISHER_MEMORY_LIMIT DATASET_PUBLISHER_JOB_BACKOFF_LIMIT \
    DATASET_PUBLISHER_JOB_ACTIVE_DEADLINE_SECONDS DATASET_PUBLISHER_JOB_TTL_SECONDS \
    DATASET_PUBLISHER_CLIENT_MAX_ATTEMPTS DATASET_PUBLISHER_INITIAL_RETRY_SECONDS \
    DATASET_PUBLISHER_MAXIMUM_RETRY_SECONDS DATASET_PUBLISHER_POLL_INTERVAL_SECONDS \
    DATASET_PUBLISHER_WORKING_DIRECTORY DATASET_PUBLISHER_PREFERRED_NODE_SELECTOR \
    DATASET_PUBLISHER_NODE_SELECTOR DATASET_PUBLISHER_TOLERATIONS_JSON; do
    require_literal "$CONFIG_TEMPLATE" "  ${key}:" \
      "publisher ConfigMap is missing ${key}"
  done

  require_literal "$SCHEDULING_TEMPLATE" 'kind: PriorityClass' \
    'publisher scheduling must create its low PriorityClass'
  require_literal "$SCHEDULING_TEMPLATE" 'kind: ResourceFlavor' \
    'publisher scheduling must create a dedicated CPU ResourceFlavor'
  require_literal "$SCHEDULING_TEMPLATE" 'kind: ClusterQueue' \
    'publisher scheduling must create a dedicated CPU ClusterQueue'
  require_literal "$SCHEDULING_TEMPLATE" 'kind: LocalQueue' \
    'publisher scheduling must create its namespace-local Kueue queue'
  require_literal "$SCHEDULING_TEMPLATE" 'clusterQueue: {{ $clusterQueueName }}' \
    'publisher LocalQueue must use the dedicated publisher ClusterQueue'
  require_literal "$SCHEDULING_TEMPLATE" 'coveredResources: ["cpu", "memory"]' \
    'publisher ClusterQueue must account only for CPU and memory'
  reject_pattern "$SCHEDULING_TEMPLATE" 'nvidia\.com/gpu|\.Values\.kueue\.(clusterQueueName|resourceFlavorName)' \
    'publisher scheduling must not reuse the GPU queue or flavor'
}

render_publisher_templates() {
  local destination="$1"
  local release_name="$2"
  local namespace="$3"
  shift 3

  helm template "$release_name" "$CHART" \
    --namespace "$namespace" \
    --values "$PROFILE" \
    --set-string "namespace=${namespace}" \
    --set kueue.enabled=false \
    --set-string tos.bucket=publisher-source \
    --set-string tos.endpoint=tos-cn-shanghai.ivolces.com \
    --set-string tos.region=cn-shanghai \
    --set-string datasetPublisher.image.digest=sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc \
    "$@" | awk '
      function emit_publisher_document() {
        if (document ~ /app.kubernetes.io\/component: dataset-publisher/) {
          printf "%s", document
        }
        document=""
      }
      /^---$/ { emit_publisher_document() }
      { document=document $0 ORS }
      END { emit_publisher_document() }
    ' >"$destination"
}

resource_name() {
  local manifest="$1"
  local expected_kind="$2"

  awk -v expected_kind="$expected_kind" '
    /^---$/ { kind=""; in_metadata=0; next }
    /^kind:/ { kind=$2; next }
    kind == expected_kind && /^metadata:$/ { in_metadata=1; next }
    in_metadata && /^  name:/ {
      name=$0
      sub(/^  name:[[:space:]]*/, "", name)
      gsub(/^"|"$/, "", name)
      print name
      exit
    }
  ' "$manifest"
}

assert_dns_label() {
  local name="$1"
  local description="$2"

  [[ -n "$name" ]] || fail "${description} name is empty"
  [[ "${#name}" -le 63 ]] || fail "${description} name exceeds 63 characters: ${name}"
  [[ "$name" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]] || \
    fail "${description} name is not DNS-label safe: ${name}"
}

assert_release_scoped_cluster_names() {
  local manifest="$1"
  local release_name="$2"
  local kind
  local name

  for kind in PriorityClass ResourceFlavor ClusterQueue; do
    name="$(resource_name "$manifest" "$kind")"
    assert_dns_label "$name" "$kind"
    [[ "$name" == "${release_name}-"* ]] || \
      fail "${kind} default name is not scoped to release ${release_name}: ${name}"
  done
}

assert_distinct_cluster_names() {
  local first_manifest="$1"
  local second_manifest="$2"
  local first_release="$3"
  local second_release="$4"
  local kind
  local first_name
  local second_name

  for kind in PriorityClass ResourceFlavor ClusterQueue; do
    first_name="$(resource_name "$first_manifest" "$kind")"
    second_name="$(resource_name "$second_manifest" "$kind")"
    [[ "$first_name" != "$second_name" ]] || \
      fail "${kind} collides between releases ${first_release} and ${second_release}: ${first_name}"
  done
}

assert_kind_count() {
  local manifest="$1"
  local kind="$2"
  local expected="$3"
  local actual

  actual="$(grep -Ec "^kind: ${kind}$" "$manifest" || true)"
  [[ "$actual" -eq "$expected" ]] || \
    fail "expected ${expected} ${kind} resource(s), found ${actual}"
}

assert_only_namespace() {
  local manifest="$1"
  local expected="$2"

  awk -v expected="$expected" '
    /^[[:space:]]*namespace:[[:space:]]*/ {
      actual=$0
      sub(/^[[:space:]]*namespace:[[:space:]]*/, "", actual)
      gsub(/^"|"$/, "", actual)
      found=1
      if (actual != expected) exit 1
    }
    END { if (!found) exit 1 }
  ' "$manifest" || fail "all publisher resources and subjects must stay in namespace ${expected}"
}

assert_rendered_contract() {
  local disabled_render="$1"
  local first_release_render="$2"
  local second_release_render="$3"
  local long_release_render="$4"
  local irsa_render="$5"
  local placement_render="$6"
  local role_trn='trn:iam::2100000000000000000:role/ray-dataset-publisher'
  local first_release='publisher-a'
  local second_release='publisher-b'
  local long_release='publisher-contract-release-abcdefghijklmnopqrstuvwxyz'
  local publisher_name='publisher-a-ray-train-platform-dataset-publisher'
  local local_queue_name='publisher-a-ray-train-platform-dataset-publisher-queue'
  local priority_class_name='publisher-a-ray-train-platform-dataset-publisher-priority'
  local resource_flavor_name='publisher-a-ray-train-platform-dataset-publisher-flavor'
  local cluster_queue_name='publisher-a-ray-train-platform-dataset-publisher-clusterqueue'

  render_publisher_templates "$disabled_render" publisher-disabled publication-disabled
  if grep -Eq '^kind:' "$disabled_render"; then
    fail 'disabled datasetPublisher.enabled must render no publisher resources'
  fi

  render_publisher_templates "$first_release_render" "$first_release" publication-a \
    --set datasetPublisher.enabled=true \
    --set backend.datasetVersioningEnabled=true \
    --set-string datasetPublisher.image.tag=test

  assert_kind_count "$first_release_render" ServiceAccount 1
  assert_kind_count "$first_release_render" Role 1
  assert_kind_count "$first_release_render" RoleBinding 1
  assert_kind_count "$first_release_render" ConfigMap 1
  assert_kind_count "$first_release_render" PriorityClass 1
  assert_kind_count "$first_release_render" ResourceFlavor 1
  assert_kind_count "$first_release_render" ClusterQueue 1
  assert_kind_count "$first_release_render" LocalQueue 1
  assert_only_namespace "$first_release_render" publication-a

  grep -Fq "name: ${publisher_name}" "$first_release_render" || \
    fail 'publisher resources must use the chart fullname convention'
  grep -Fq "DATASET_PUBLISHER_SERVICE_ACCOUNT: \"${publisher_name}\"" "$first_release_render" || \
    fail 'ConfigMap must identify the dedicated publisher ServiceAccount'
  if grep -Eq '^kind: (Secret|ClusterRole|ClusterRoleBinding)$' "$first_release_render"; then
    fail 'publisher render must not create Secrets or cluster-wide RBAC'
  fi
  require_literal "$first_release_render" 'automountServiceAccountToken: false' \
    'publisher workload ServiceAccount must not mount a Kubernetes API token'
  awk '
    /^kind: RoleBinding$/ { in_binding=1 }
    in_binding && /^subjects:$/ { in_subjects=1; next }
    in_subjects && /^roleRef:$/ { in_subjects=0 }
    in_subjects && /^[[:space:]]*- kind: ServiceAccount$/ { subject_count++ }
    in_subjects && /^[[:space:]]*name: ray-train-platform-sa$/ { backend_count++ }
    END {
      if (!in_binding || subject_count != 1 || backend_count != 1) exit 1
    }
  ' "$first_release_render" || fail 'publisher Job permissions must bind only the backend controller identity'
  if grep -Eiq 'nvidia\.com/gpu|secretKeyRef|envFrom' "$first_release_render"; then
    fail 'publisher render must not request GPUs or consume Secrets'
  fi
  if grep -Fq 'vke.volcengine.com/role-trn:' "$first_release_render"; then
    fail 'empty IRSA role TRN must not render an annotation'
  fi
  if grep -Fq 'DATASET_PUBLISHER_IRSA_ROLE_TRN:' "$first_release_render"; then
    fail 'empty IRSA role TRN must not render backend IRSA configuration'
  fi

  require_literal "$first_release_render" 'DATASET_PUBLISHER_IMAGE: "registry.invalid/ray-platform/ray-dataset-publisher@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"' \
    'ConfigMap must resolve the publisher image through the global registry'
  require_literal "$first_release_render" "DATASET_PUBLISHER_QUEUE_NAME: \"${local_queue_name}\"" \
    'ConfigMap must provide the low-priority Kueue LocalQueue'
  require_literal "$first_release_render" "DATASET_PUBLISHER_PRIORITY_CLASS_NAME: \"${priority_class_name}\"" \
    'ConfigMap must provide the low-priority PriorityClass'
  require_literal "$first_release_render" "clusterQueue: ${cluster_queue_name}" \
    'publisher LocalQueue must attach to its dedicated CPU ClusterQueue'
  require_literal "$first_release_render" "name: ${resource_flavor_name}" \
    'publisher ResourceFlavor must have a dedicated name'
  require_literal "$first_release_render" 'coveredResources: ["cpu", "memory"]' \
    'publisher ClusterQueue must cover only CPU and memory'
  require_literal "$first_release_render" 'nodeLabels: {}' \
    'publisher ResourceFlavor must allow CPU and memory fallback on GPU nodes'
  require_literal "$first_release_render" 'kubernetes.io/metadata.name: "publication-a"' \
    'publisher ClusterQueue must be scoped to the publication namespace'
  require_literal "$first_release_render" 'nominalQuota: "16"' \
    'publisher ClusterQueue must render its CPU quota'
  require_literal "$first_release_render" 'nominalQuota: "64Gi"' \
    'publisher ClusterQueue must render its memory quota'
  require_literal "$first_release_render" 'value: -1000' \
    'publisher PriorityClass must remain below normal training priority'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_SOURCE_BUCKET: "publisher-source"' \
    'ConfigMap must provide the scoped source bucket'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_TARGET_BUCKET: "publisher-source"' \
    'ConfigMap must default the target bucket to the source bucket'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_ENDPOINT: "tos-cn-shanghai.ivolces.com"' \
    'ConfigMap must provide a bare approved TOS endpoint'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_REGION: "cn-shanghai"' \
    'ConfigMap must provide the TOS region'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_CPU_REQUEST: "1000m"' \
    'ConfigMap must provide a bounded CPU request'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_CPU_LIMIT: "4000m"' \
    'ConfigMap must provide a bounded CPU limit'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_MEMORY_REQUEST: "2Gi"' \
    'ConfigMap must provide a bounded memory request'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_MEMORY_LIMIT: "8Gi"' \
    'ConfigMap must provide a bounded memory limit'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_JOB_BACKOFF_LIMIT: "3"' \
    'ConfigMap must provide the Job backoff limit'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_JOB_ACTIVE_DEADLINE_SECONDS: "604800"' \
    'ConfigMap must provide the Job active deadline'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_JOB_TTL_SECONDS: "86400"' \
    'ConfigMap must provide the Job TTL'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_CLIENT_MAX_ATTEMPTS: "3"' \
    'ConfigMap must provide the Kubernetes client attempt limit'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_INITIAL_RETRY_SECONDS: "1"' \
    'ConfigMap must provide the Kubernetes client initial retry backoff'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_MAXIMUM_RETRY_SECONDS: "30"' \
    'ConfigMap must provide the Kubernetes client maximum retry backoff'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_POLL_INTERVAL_SECONDS: "10"' \
    'ConfigMap must provide the publisher manager polling interval'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_WORKING_DIRECTORY: "/data/output"' \
    'ConfigMap must provide the non-root publisher working directory'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_PREFERRED_NODE_SELECTOR: ""' \
    'default publisher CPU-node preference must remain portable'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_NODE_SELECTOR: ""' \
    'default publisher nodeSelector must allow GPU-node CPU fallback'
  require_literal "$first_release_render" 'DATASET_PUBLISHER_TOLERATIONS_JSON: "[]"' \
    'default publisher tolerations must be empty'

  render_publisher_templates "$second_release_render" "$second_release" publication-b \
    --set datasetPublisher.enabled=true \
    --set backend.datasetVersioningEnabled=true \
    --set-string datasetPublisher.image.tag=test
  render_publisher_templates "$long_release_render" "$long_release" publication-long \
    --set datasetPublisher.enabled=true \
    --set backend.datasetVersioningEnabled=true \
    --set-string datasetPublisher.image.tag=test

  assert_only_namespace "$second_release_render" publication-b
  assert_only_namespace "$long_release_render" publication-long
  assert_release_scoped_cluster_names "$first_release_render" "$first_release"
  assert_release_scoped_cluster_names "$second_release_render" "$second_release"
  assert_release_scoped_cluster_names "$long_release_render" "$long_release"
  assert_distinct_cluster_names "$first_release_render" "$second_release_render" "$first_release" "$second_release"
  assert_distinct_cluster_names "$first_release_render" "$long_release_render" "$first_release" "$long_release"
  assert_dns_label "$(resource_name "$long_release_render" LocalQueue)" 'long-release LocalQueue'
  assert_dns_label "$(resource_name "$long_release_render" ServiceAccount)" 'long-release ServiceAccount'

  render_publisher_templates "$irsa_render" publisher-irsa publication-system \
    --set datasetPublisher.enabled=true \
    --set-string datasetPublisher.image.tag=test \
    --set-string "datasetPublisher.serviceAccount.irsaRoleTRN=${role_trn}"
  require_literal "$irsa_render" 'vke.volcengine.com/role-trn: "trn:iam::2100000000000000000:role/ray-dataset-publisher"' \
    'non-empty Volcengine IRSA role TRN must render on the dedicated ServiceAccount'
  require_literal "$irsa_render" 'DATASET_PUBLISHER_IRSA_ROLE_TRN: "trn:iam::2100000000000000000:role/ray-dataset-publisher"' \
    'non-empty Volcengine IRSA role TRN must flow through the non-secret publisher ConfigMap'
  if grep -Eiq 'kind:[[:space:]]*Secret|secretKeyRef|secretRef|access[_-]?key|secret[_-]?key' "$irsa_render"; then
    fail 'configured publisher IRSA render must not create or consume static credential Secrets'
  fi

  render_publisher_templates "$placement_render" publisher-placement publication-system \
    --set datasetPublisher.enabled=true \
    --set-string datasetPublisher.image.tag=test \
    --set-string datasetPublisher.preferredNodeSelector.pool=cpu \
    --set-string datasetPublisher.nodeSelector.lifecycle=shared \
    --set-string 'datasetPublisher.tolerations[0].key=dedicated' \
    --set-string 'datasetPublisher.tolerations[0].operator=Equal' \
    --set-string 'datasetPublisher.tolerations[0].value=platform' \
    --set-string 'datasetPublisher.tolerations[0].effect=NoSchedule'
  require_literal "$placement_render" 'DATASET_PUBLISHER_PREFERRED_NODE_SELECTOR: "pool=cpu"' \
    'ConfigMap must carry an optional soft CPU-node preference'
  require_literal "$placement_render" 'DATASET_PUBLISHER_NODE_SELECTOR: "lifecycle=shared"' \
    'ConfigMap must carry an optional explicit nodeSelector'
  grep -Eq 'DATASET_PUBLISHER_TOLERATIONS_JSON: .*dedicated.*NoSchedule.*Equal.*platform|DATASET_PUBLISHER_TOLERATIONS_JSON: .*NoSchedule.*dedicated.*Equal.*platform' "$placement_render" || \
    fail 'ConfigMap must carry optional tolerations'
}

assert_invalid_irsa_role_rejected() {
  local invalid_render="$1"
  local error_output

  if error_output="$(render_publisher_templates "$invalid_render" publisher-invalid-irsa publication-system \
    --set datasetPublisher.enabled=true \
    --set-string datasetPublisher.image.tag=test \
    --set-string datasetPublisher.serviceAccount.irsaRoleTRN=trn:iam::2103446203:user/not-a-role 2>&1)"; then
    fail 'malformed Volcengine IRSA role TRN must fail Helm rendering'
  fi
  grep -Fq 'datasetPublisher.serviceAccount.irsaRoleTRN must be a valid Volcengine IAM role TRN' <<<"$error_output" || \
    fail 'malformed publisher IRSA role must fail with a sanitized validation error'
}

assert_source_contract

if ! command -v helm >/dev/null 2>&1; then
  echo 'Helm is required for the dataset publisher rendered-template contract; static assertions passed, render NOT RUN' >&2
  exit 1
fi

disabled_render="$(mktemp)"
first_release_render="$(mktemp)"
second_release_render="$(mktemp)"
long_release_render="$(mktemp)"
irsa_render="$(mktemp)"
placement_render="$(mktemp)"
invalid_irsa_render="$(mktemp)"
trap 'rm -f "$disabled_render" "$first_release_render" "$second_release_render" "$long_release_render" "$irsa_render" "$placement_render" "$invalid_irsa_render"' EXIT

assert_rendered_contract "$disabled_render" "$first_release_render" "$second_release_render" \
  "$long_release_render" "$irsa_render" "$placement_render"
assert_invalid_irsa_role_rejected "$invalid_irsa_render"

echo 'Dataset publisher rendered-template contract verified'
