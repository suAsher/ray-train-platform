#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly CHART="${ROOT_DIR}/helm/ray-train-platform"
readonly VALUES="${CHART}/values.yaml"
readonly TEMPLATE="${CHART}/templates/backend-deployment.yaml"
readonly PROFILE="${ROOT_DIR}/deploy/profiles/test.yaml"

assert_source_line() {
  local file="$1"
  local expected="$2"
  local message="$3"

  grep -Fqx -- "$expected" "$file" || {
    echo "$message" >&2
    exit 1
  }
}

assert_source_contract() {
  assert_source_line "$VALUES" '  datasetVersioningEnabled: false' \
    'base values must disable dataset versioning by default'
  assert_source_line "$VALUES" '  rayDataStreamingEnabled: false' \
    'base values must disable Ray Data streaming by default'
  assert_source_line "$VALUES" '  datasetInternalPrefix: ray-train/platform/datasets' \
    'base values must use the platform-owned dataset prefix'
  assert_source_line "$TEMPLATE" '              value: {{ default false .Values.backend.datasetVersioningEnabled | quote }}' \
    'backend template must safely default DATASET_VERSIONING_ENABLED to false'
  assert_source_line "$TEMPLATE" '              value: {{ default false .Values.backend.rayDataStreamingEnabled | quote }}' \
    'backend template must safely default RAY_DATA_STREAMING_ENABLED to false'
  assert_source_line "$TEMPLATE" '              value: {{ default "ray-train/platform/datasets" .Values.backend.datasetInternalPrefix | quote }}' \
    'backend template must safely default DATASET_INTERNAL_PREFIX'

  if grep -Eiq 'publisher' "$VALUES" "$TEMPLATE"; then
    echo 'disabled feature configuration must not introduce publisher identity or resources' >&2
    exit 1
  fi
}

assert_rendered_env() {
  local manifest="$1"
  local name="$2"
  local expected="$3"
  local count

  count="$(grep -Ec "^[[:space:]]*- name: ${name}$" "$manifest" || true)"
  if [[ "$count" -ne 1 ]]; then
    echo "rendered chart must contain exactly one ${name} entry; found ${count}" >&2
    exit 1
  fi

  awk -v name="$name" -v expected="$expected" '
    $0 ~ "^[[:space:]]*- name: " name "$" { found_name=1; next }
    found_name {
      value=$0
      sub(/^[[:space:]]*value:[[:space:]]*/, "", value)
      exit value != "\"" expected "\""
    }
    END { if (!found_name) exit 1 }
  ' "$manifest" || {
    echo "rendered backend is missing ${name}=${expected}" >&2
    exit 1
  }
}

normalize_feature_env_values() {
  local source="$1"
  local destination="$2"

  awk '
    /^[[:space:]]*- name: (DATASET_VERSIONING_ENABLED|RAY_DATA_STREAMING_ENABLED|DATASET_INTERNAL_PREFIX)$/ {
      feature_value=1
      print
      next
    }
    feature_value && /^[[:space:]]*value:/ {
      sub(/value:.*/, "value: \"<feature-value>\"")
      feature_value=0
      print
      next
    }
    { feature_value=0; print }
  ' "$source" >"$destination"
}

assert_source_contract

if ! command -v helm >/dev/null 2>&1; then
  echo 'Helm is required for the feature-flag rendered-template contract; static assertions passed, render NOT RUN' >&2
  exit 1
fi

disabled_render="$(mktemp)"
enabled_render="$(mktemp)"
disabled_normalized="$(mktemp)"
enabled_normalized="$(mktemp)"
trap 'rm -f "$disabled_render" "$enabled_render" "$disabled_normalized" "$enabled_normalized"' EXIT

helm template ray-train-platform "$CHART" \
  --namespace ray-train-platform \
  --values "$PROFILE" >"$disabled_render"
helm template ray-train-platform "$CHART" \
  --namespace ray-train-platform \
  --values "$PROFILE" \
  --set backend.datasetVersioningEnabled=true \
  --set backend.rayDataStreamingEnabled=true \
  --set-string backend.datasetInternalPrefix=private/platform/datasets >"$enabled_render"

assert_rendered_env "$disabled_render" DATASET_VERSIONING_ENABLED false
assert_rendered_env "$disabled_render" RAY_DATA_STREAMING_ENABLED false
assert_rendered_env "$disabled_render" DATASET_INTERNAL_PREFIX ray-train/platform/datasets
assert_rendered_env "$enabled_render" DATASET_VERSIONING_ENABLED true
assert_rendered_env "$enabled_render" RAY_DATA_STREAMING_ENABLED true
assert_rendered_env "$enabled_render" DATASET_INTERNAL_PREFIX private/platform/datasets

if grep -Eiq 'publisher' "$disabled_render"; then
  echo 'disabled rendered chart must not contain publisher identity or resources' >&2
  exit 1
fi

normalize_feature_env_values "$disabled_render" "$disabled_normalized"
normalize_feature_env_values "$enabled_render" "$enabled_normalized"
cmp -s "$disabled_normalized" "$enabled_normalized" || {
  echo 'feature flag overrides must change only their backend environment values' >&2
  exit 1
}

echo 'Feature-flag rendered-template contract verified'
