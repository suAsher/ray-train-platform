#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly CHART="${ROOT_DIR}/helm/ray-train-platform"
readonly VALUES="${CHART}/values.yaml"
readonly POD_MONITOR="${CHART}/templates/dataset-podmonitor.yaml"
readonly PROMETHEUS_RULE="${CHART}/templates/dataset-prometheusrule.yaml"
readonly PROFILE="${ROOT_DIR}/deploy/profiles/test.yaml"

fail() {
  echo "dataset monitoring render contract: $*" >&2
  exit 1
}

require_literal() {
  local file="$1"
  local expected="$2"
  local message="$3"
  [[ -f "$file" ]] || fail "missing ${file#"${ROOT_DIR}/"}"
  grep -Fq -- "$expected" "$file" || fail "$message"
}

require_literal "$VALUES" 'monitoring:' 'monitoring values are missing'
require_literal "$VALUES" '  enabled: false' 'dataset monitoring must be default-off'
require_literal "$VALUES" '    enabled: false' 'dataset PodMonitor must require explicit opt-in to avoid duplicate scraping'
require_literal "$POD_MONITOR" 'kind: PodMonitor' 'Ray worker metrics require a PodMonitor'
require_literal "$POD_MONITOR" 'namespaceSelector:' 'PodMonitor must discover tenant namespaces'
require_literal "$POD_MONITOR" 'any: true' 'PodMonitor must discover future tenant namespaces'
require_literal "$POD_MONITOR" 'port: metrics' 'PodMonitor must scrape the named Ray metrics port'
require_literal "$POD_MONITOR" 'values: [worker]' 'only Ray workers should be scraped for managed data metrics'
require_literal "$POD_MONITOR" '__meta_kubernetes_pod_label_platform_job_id' 'job identity must come from trusted Pod labels'
require_literal "$POD_MONITOR" 'targetLabel: platform_job_id' 'scraped metrics must retain the platform job identity'
require_literal "$POD_MONITOR" 'action: labeldrop' 'sensitive labels must be removed at ingestion'
require_literal "$PROMETHEUS_RULE" 'ray_platform_training_dataset_batches_total{data_mode="streaming"}' \
  'native Ray spill alerts must join against a governed streaming-job metric'

for alert in \
  RayDatasetPublicationFailed \
  RayDatasetPublicationStalled \
  RayDatasetSourceReadP95High \
  RayDatasetCacheHitRatioLow \
  RayDatasetIntegrityFailure \
  RayDatasetCacheEvictionThrash \
  RayDatasetPrefetchWaitHigh \
  RayDatasetObjectSpillHigh \
  RayDatasetGPUDataStall; do
  require_literal "$PROMETHEUS_RULE" "alert: ${alert}" "missing ${alert}"
done

for metric in \
  ray_platform_training_dataset_source_read_p95_seconds \
  ray_platform_training_dataset_cache_hits_total \
  ray_platform_training_dataset_cache_misses_total \
  ray_platform_training_dataset_cache_checksum_failures_total \
  ray_platform_training_dataset_cache_evictions_total \
  ray_platform_training_dataset_prefetch_wait_seconds_total \
  ray_object_store_spilled_bytes_total \
  ray_platform_training_data_time_seconds \
  ray_platform_training_step_time_seconds \
  DCGM_FI_DEV_GPU_UTIL; do
  require_literal "$PROMETHEUS_RULE" "$metric" "missing metric ${metric}"
done

if grep -Eiq 'by[[:space:]]*\([^)]*(object_key|token|access_key|secret_key|storage_uri|manifest_uri)' "$PROMETHEUS_RULE"; then
  fail 'unbounded or sensitive values must not be Prometheus grouping labels'
fi

command -v helm >/dev/null 2>&1 || fail 'Helm is required for the rendered-template contract'

disabled_render="$(mktemp)"
enabled_render="$(mktemp)"
pod_monitor_render="$(mktemp)"
rules_disabled_render="$(mktemp)"
trap 'rm -f "$disabled_render" "$enabled_render" "$pod_monitor_render" "$rules_disabled_render"' EXIT

helm template ray-platform "$CHART" --namespace ray-train-platform --values "$PROFILE" >"$disabled_render"
helm template ray-platform "$CHART" --namespace ray-train-platform --values "$PROFILE" \
  --set monitoring.enabled=true >"$enabled_render"
helm template ray-platform "$CHART" --namespace ray-train-platform --values "$PROFILE" \
  --set monitoring.enabled=true \
  --set monitoring.podMonitor.enabled=true >"$pod_monitor_render"
helm template ray-platform "$CHART" --namespace ray-train-platform --values "$PROFILE" \
  --set monitoring.enabled=true \
  --set monitoring.prometheusRule.enabled=false >"$rules_disabled_render"

if grep -Eq '^kind: (PodMonitor|PrometheusRule)$' "$disabled_render"; then
  fail 'default values must not render monitoring CRDs'
fi
[[ "$(grep -Ec '^kind: PrometheusRule$' "$enabled_render")" -eq 1 ]] || fail 'enabled chart must render one PrometheusRule'
if grep -Eq '^kind: PodMonitor$' "$enabled_render"; then
  fail 'monitoring alone must not duplicate an existing cluster-wide Ray PodMonitor'
fi
[[ "$(grep -Ec '^kind: PodMonitor$' "$pod_monitor_render")" -eq 1 ]] || fail 'explicit PodMonitor opt-in must render one PodMonitor'
[[ "$(grep -Ec '^kind: PrometheusRule$' "$pod_monitor_render")" -eq 1 ]] || fail 'explicit PodMonitor opt-in must retain one PrometheusRule'
if grep -Eq '^kind: PrometheusRule$' "$rules_disabled_render"; then
  fail 'PrometheusRule must be independently disableable'
fi

for forbidden in object_key access_token secret_key storage_uri manifest_uri; do
  if grep -Eq "(^|[,{[:space:]])${forbidden}=" "$pod_monitor_render"; then
    fail "rendered PromQL exposes forbidden label ${forbidden}"
  fi
done

echo 'Dataset monitoring rendered-template contract verified'
