#!/usr/bin/env bash
set -euo pipefail

readonly chart_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
command -v helm >/dev/null || { echo 'missing command: helm' >&2; exit 1; }
readonly rendered_dir="$(mktemp -d)"
trap 'rm -f -- "${rendered_dir}"/*.yaml "${rendered_dir}/error"; rmdir -- "${rendered_dir}"' EXIT

extract_configmap() {
  awk '/^# Source: .*\/templates\/configmap.yaml$/ { inside = 1; next }
       inside && /^---$/ { exit }
       inside { print }' "$1"
}

for profile in default vke-data1 vke-data2; do
  profile_args=(--values "${chart_dir}/values.yaml")
  if [[ "${profile}" != default ]]; then
    profile_args=(--values "${chart_dir}/values-${profile}.yaml")
  fi
  helm template cache "${chart_dir}" --namespace ray-cache-local "${profile_args[@]}" \
    --set monitoring.enabled=true >"${rendered_dir}/default.yaml"
  helm template cache "${chart_dir}" --namespace ray-cache-local "${profile_args[@]}" \
    --set monitoring.enabled=true --set-string existingConfigMap= >"${rendered_dir}/empty.yaml"
  cmp "${rendered_dir}/default.yaml" "${rendered_dir}/empty.yaml"
  helm template cache "${chart_dir}" --namespace ray-cache-local "${profile_args[@]}" \
    --set monitoring.enabled=true --set-string existingConfigMap=controller-cache.example \
    >"${rendered_dir}/external.yaml"

  # Deployment config mount, provisioner reload argument and monitor script
  # mount must select one complete external map; Helm must not create that map.
  [[ "$(grep -Fc 'controller-cache.example' "${rendered_dir}/external.yaml")" -eq 3 ]]
  [[ "$(grep -Ec '^[[:space:]]+name: "controller-cache.example"$' "${rendered_dir}/external.yaml")" -eq 2 ]]
  [[ "$(grep -Ec '^[[:space:]]+- "controller-cache.example"$' "${rendered_dir}/external.yaml")" -eq 1 ]]
  extract_configmap "${rendered_dir}/default.yaml" >"${rendered_dir}/legacy-default.yaml"
  extract_configmap "${rendered_dir}/external.yaml" >"${rendered_dir}/legacy-external.yaml"
  [[ -s "${rendered_dir}/legacy-default.yaml" ]]
  cmp "${rendered_dir}/legacy-default.yaml" "${rendered_dir}/legacy-external.yaml"
done

for invalid in 'UPPER' 'a_b' '-name' 'name-' 'a..b' 'a.-b' 'a-.b' '{{ .Release.Name }}'; do
  if helm template cache "${chart_dir}" --set-string "existingConfigMap=${invalid}" >"${rendered_dir}/external.yaml" 2>"${rendered_dir}/error"; then
    echo "invalid existingConfigMap accepted: ${invalid}" >&2
    exit 1
  fi
done
# Non-string values must fail even when Helm treats them as false/empty.
for invalid in false 42; do
  if helm template cache "${chart_dir}" --set "existingConfigMap=${invalid}" >"${rendered_dir}/external.yaml" 2>"${rendered_dir}/error"; then
    echo "non-string existingConfigMap accepted: ${invalid}" >&2
    exit 1
  fi
  grep -Fq 'existingConfigMap must be a string' "${rendered_dir}/error"
done

echo 'external ConfigMap contract verified'
