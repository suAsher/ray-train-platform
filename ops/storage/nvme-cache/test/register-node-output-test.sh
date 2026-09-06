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
if [[ "$*" == *configmap* ]]; then
  disk=data1
  [[ "$*" != *data2* ]] || disk=data2
  [[ "${FAIL_CASE:-}" != malformed ]] || { printf 'bad-json'; exit; }
  [[ "${FAIL_CASE:-}" != mixed ]] || disk=data1
  jq -n --arg path "/${disk}/ray-cache" '{data:{"config.json":({nodePathMap:[{node:"existing-3",paths:[$path]},{node:"existing-4",paths:[$path]},{node:"DEFAULT_PATH_FOR_NON_LISTED_NODES",paths:[]}]} | tojson)}}'
  exit
fi
[[ "${FAIL_CASE:-}" != uncordoned ]] || { printf '{"spec":{},"status":{"conditions":[{"type":"Ready","status":"True"}]}}'; exit; }
cat <<'JSON'
{"spec":{"unschedulable":true},"metadata":{"labels":{}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}
JSON
EOF
cat >"${mock_bin}/ssh" <<'EOF'
#!/usr/bin/env bash
[[ "${FAIL_CASE:-}" != ssh ]] || exit 1
if [[ "$*" == *findmnt* ]]; then
  [[ "${FAIL_CASE:-}" != rootfs ]] || { printf '/ /dev/root 8:1\n'; exit; }
  [[ "${FAIL_CASE:-}" != same ]] || { [[ "$*" == *data1* ]] && printf '/data1 ' || printf '/data2 '; printf '/dev/nvme1 259:1\n'; exit; }
fi
case "$*" in
  *findmnt*data1*) printf '/data1 /dev/nvme1 259:1\n' ;;
  *findmnt*data2*) printf '/data2 /dev/nvme2 259:2\n' ;;
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
for disk in data1 data2; do
  jq -e --arg path "/${disk}/ray-cache" '.nodePathMap | length == 3 and all(.[]; .paths == [$path]) and any(.[]; .node == "existing-3") and any(.[]; .node == "existing-4") and any(.[]; .node == "172.28.1.240")' "${output_dir}/${disk}-values-patch.yaml" >/dev/null
done
[[ -f "${output_dir}/acceptance-report.txt" ]]
if find "${output_dir}" -mindepth 1 -maxdepth 1 -name '.staging.*' | grep -q .; then
  echo 'register-node left a staging directory behind' >&2
  exit 1
fi

readonly report_before="$(cksum "${output_dir}/acceptance-report.txt")"
readonly patch_before="$(cksum "${output_dir}/data1-values-patch.yaml")"
if PATH="${mock_bin}:${PATH}" bash "${register_script}" \
  --node 172.28.1.240 --output-dir "${output_dir}" >/dev/null 2>&1; then
  echo 'register-node overwrote an existing output directory' >&2
  exit 1
fi
[[ "$(cksum "${output_dir}/acceptance-report.txt")" == "${report_before}" ]]
[[ "$(cksum "${output_dir}/data1-values-patch.yaml")" == "${patch_before}" ]]

for failure in malformed mixed uncordoned rootfs same ssh; do
  failed_output="${test_root}/failed-${failure}"
  if FAIL_CASE="${failure}" PATH="${mock_bin}:${PATH}" bash "${register_script}" --node new-node --output-dir "${failed_output}" >/dev/null 2>&1; then
    echo "accepted ${failure}" >&2; exit 1
  fi
  [[ ! -e "${failed_output}/data1-values-patch.yaml" && ! -e "${failed_output}/data2-values-patch.yaml" ]]
done

if PATH="${mock_bin}:${PATH}" bash "${register_script}" --node existing-3 --output-dir "${test_root}/already-registered" >/dev/null 2>&1; then
  echo 'accepted already registered node' >&2; exit 1
fi
[[ ! -e "${test_root}/already-registered/data1-values-patch.yaml" ]]

echo 'register-node output safety contract verified'
