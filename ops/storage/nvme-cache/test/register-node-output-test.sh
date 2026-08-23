#!/usr/bin/env bash
set -euo pipefail

readonly ops_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly register_script="${ops_dir}/register-node.sh"
readonly test_root="$(mktemp -d)"
trap 'rm -rf -- "${test_root}"' EXIT

require_source() {
  local expected="$1"
  grep -Fq -- "${expected}" "${register_script}" || {
    echo "register-node missing output safety contract: ${expected}" >&2
    exit 1
  }
}

require_source 'mkdir -m 0700'
require_source 'set -o noclobber'
require_source 'mktemp -d'
require_source 'mv -n'
if grep -Fq -- 'mkdir -p "${output_dir}"' "${register_script}"; then
  echo 'register-node must not reuse an existing output directory' >&2
  exit 1
fi

existing_dir="${test_root}/existing"
mkdir "${existing_dir}"
if bash "${register_script}" --node 172.28.1.240 --output-dir "${existing_dir}" >/dev/null 2>&1; then
  echo 'register-node accepted an existing output directory' >&2
  exit 1
fi

symlink_target="${test_root}/symlink-target"
symlink_output="${test_root}/symlink-output"
mkdir "${symlink_target}"
ln -s "${symlink_target}" "${symlink_output}"
if bash "${register_script}" --node 172.28.1.240 --output-dir "${symlink_output}" >/dev/null 2>&1; then
  echo 'register-node accepted a symlink output directory' >&2
  exit 1
fi

readonly mock_bin="${test_root}/bin"
mkdir "${mock_bin}"
cat >"${mock_bin}/kubectl" <<'EOF'
#!/usr/bin/env bash
cat <<'JSON'
{"metadata":{"labels":{"accelerator":"nvidia-rtx-4090","gpu-pool":"production"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}
JSON
EOF
cat >"${mock_bin}/ssh" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  *findmnt*) printf '/dev/nvme-test\n' ;;
  *'df -Pk'*) printf '50.00\n' ;;
esac
EOF
chmod +x "${mock_bin}/kubectl" "${mock_bin}/ssh"

output_dir="${test_root}/new-output"
PATH="${mock_bin}:${PATH}" bash "${register_script}" \
  --node 172.28.1.240 --output-dir "${output_dir}" >/dev/null

if mode="$(stat -c '%a' "${output_dir}" 2>/dev/null)"; then
  :
else
  mode="$(stat -f '%Lp' "${output_dir}")"
fi
[[ "${mode}" == '700' ]] || {
  echo "register-node output directory mode is ${mode}, expected 700" >&2
  exit 1
}
[[ -f "${output_dir}/values-patch.yaml" ]]
[[ -f "${output_dir}/acceptance-report.txt" ]]
if find "${output_dir}" -mindepth 1 -maxdepth 1 -name '.staging.*' | grep -q .; then
  echo 'register-node left a staging directory behind' >&2
  exit 1
fi

readonly report_before="$(cksum "${output_dir}/acceptance-report.txt")"
readonly patch_before="$(cksum "${output_dir}/values-patch.yaml")"
if PATH="${mock_bin}:${PATH}" bash "${register_script}" \
  --node 172.28.1.240 --output-dir "${output_dir}" >/dev/null 2>&1; then
  echo 'register-node overwrote an existing output directory' >&2
  exit 1
fi
[[ "$(cksum "${output_dir}/acceptance-report.txt")" == "${report_before}" ]]
[[ "$(cksum "${output_dir}/values-patch.yaml")" == "${patch_before}" ]]

echo 'register-node output safety contract verified'
