#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
dockerfile="${root_dir}/examples/bevfusion/Dockerfile.source-overlay"

root_line="$(grep -n '^USER root$' "${dockerfile}" | cut -d: -f1)"
ray_install_line="$(grep -n 'ray\[default\]' "${dockerfile}" | head -n1 | cut -d: -f1)"
final_user="$(tail -n1 "${dockerfile}")"

test -n "${root_line}"
test -n "${ray_install_line}"
test "${root_line}" -lt "${ray_install_line}"
test "${final_user}" = 'USER 1000:1000'

printf '%s\n' 'source overlay non-root runtime contract: ok'
