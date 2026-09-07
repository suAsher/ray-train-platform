#!/usr/bin/env bash
set -euo pipefail

readonly chart_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly helm_bin="${HELM:-helm}"
readonly rendered_dir="$(mktemp -d)"
trap 'rm -f -- "${rendered_dir}"/*.yaml "${rendered_dir}/error"; rmdir -- "${rendered_dir}"' EXIT
readonly image="registry.example/onboarding@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
readonly enabled_args=(--namespace ray-cache-local --set enabled=true --set-string "image=${image}"
  --set-string 'nfs.shares[0].server=nfs.example' --set-string 'nfs.shares[0].path=/datasets')

"${helm_bin}" lint "${chart_dir}" >/dev/null
"${helm_bin}" template node-onboarding "${chart_dir}" >"${rendered_dir}/disabled.yaml"
[[ -z "$(tr -d '[:space:]' <"${rendered_dir}/disabled.yaml")" ]]
"${helm_bin}" lint "${chart_dir}" "${enabled_args[@]}" >/dev/null
"${helm_bin}" template node-onboarding "${chart_dir}" "${enabled_args[@]}" >"${rendered_dir}/staged.yaml"
grep -Fq 'replicas: 0' "${rendered_dir}/staged.yaml"
[[ "$(grep -c '^kind: ValidatingAdmissionPolicy$' "${rendered_dir}/staged.yaml")" -eq 8 ]]
[[ "$(grep -c '^  failurePolicy: Fail$' "${rendered_dir}/staged.yaml")" -eq 8 ]]
[[ "$(grep -c '^  validationActions: \[Deny\]$' "${rendered_dir}/staged.yaml")" -eq 8 ]]
"${helm_bin}" template node-onboarding "${chart_dir}" "${enabled_args[@]}" --set activateController=true >"${rendered_dir}/active.yaml"
grep -Fq 'replicas: 1' "${rendered_dir}/active.yaml"
grep -Fq 'type: Recreate' "${rendered_dir}/active.yaml"
grep -Fq 'kind: PriorityClass' "${rendered_dir}/active.yaml"
grep -Fq 'value: -1000' "${rendered_dir}/active.yaml"
grep -Fq 'globalDefault: false' "${rendered_dir}/active.yaml"
grep -Fq 'preemptionPolicy: Never' "${rendered_dir}/active.yaml"
grep -Fq -- '--probe-service-account=node-onboarding-probe' "${rendered_dir}/active.yaml"
if grep -Eq '^kind: (Namespace|Service|DaemonSet)$' "${rendered_dir}/active.yaml"; then
  echo 'unexpected namespace or exposed service/workload' >&2
  exit 1
fi
for unsafe in 'image=registry.example/controller:latest' 'data1ConfigMap=ray-cache-local-data1-config' 'activateController=not-a-boolean'; do
  if "${helm_bin}" template node-onboarding "${chart_dir}" "${enabled_args[@]}" --set-string "${unsafe}" >"${rendered_dir}/active.yaml" 2>"${rendered_dir}/error"; then
    echo "unsafe value accepted: ${unsafe}" >&2
    exit 1
  fi
done

echo 'node onboarding Helm render contract verified (API server CEL checks still required)'
