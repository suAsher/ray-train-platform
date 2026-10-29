#!/usr/bin/env bash
set -euo pipefail

readonly chart_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly data1_values="${chart_dir}/values-example-data1.yaml"
readonly data2_values="${chart_dir}/values-example-data2.yaml"
readonly rendered1="$(mktemp)"
readonly rendered2="$(mktemp)"
trap 'rm -f -- "${rendered1}" "${rendered2}"' EXIT

helm lint "${chart_dir}" --values "${chart_dir}/tests/image-fixture.yaml" --values "${data1_values}" >/dev/null
helm lint "${chart_dir}" --values "${chart_dir}/tests/image-fixture.yaml" --values "${data2_values}" >/dev/null
helm template ray-cache-local-data1 "${chart_dir}" --values "${chart_dir}/tests/image-fixture.yaml" --namespace ray-cache-local --values "${data1_values}" >"${rendered1}"
helm template ray-cache-local-data2 "${chart_dir}" --values "${chart_dir}/tests/image-fixture.yaml" --namespace ray-cache-local --values "${data2_values}" >"${rendered2}"

require() {
  grep -Fq -- "$2" "$1" || { echo "$1 missing: $2" >&2; exit 1; }
}
reject() {
  if grep -Fq -- "$2" "$1"; then echo "$1 unexpectedly contains: $2" >&2; exit 1; fi
}

for rendered in "${rendered1}" "${rendered2}"; do
  require "${rendered}" 'reclaimPolicy: Delete'
  require "${rendered}" 'volumeBindingMode: WaitForFirstConsumer'
  require "${rendered}" 'allowVolumeExpansion: false'
  require "${rendered}" '"node": "gpu-node-a"'
  require "${rendered}" '"node": "gpu-node-b"'
  require "${rendered}" '"node": "DEFAULT_PATH_FOR_NON_LISTED_NODES"'
  require "${rendered}" '"paths": []'
done

require "${rendered1}" 'name: ray-cache-local-data1'
require "${rendered1}" 'provisioner: raytrain.io/local-path-data1'
require "${rendered1}" '/data1/ray-cache'
reject "${rendered1}" '/data2/ray-cache'
reject "${rendered1}" 'raytrain.io/local-path-data2'
reject "${rendered1}" '__RAY_CACHE_ALLOWED_ROOTS__'

require "${rendered2}" 'name: ray-cache-local-data2'
require "${rendered2}" 'provisioner: raytrain.io/local-path-data2'
require "${rendered2}" '/data2/ray-cache'
reject "${rendered2}" '/data1/ray-cache'
reject "${rendered2}" 'raytrain.io/local-path-data1'
reject "${rendered2}" '__RAY_CACHE_ALLOWED_ROOTS__'

echo 'dual NVMe provisioner render contract verified'
