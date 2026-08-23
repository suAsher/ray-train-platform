#!/usr/bin/env bash
set -euo pipefail

readonly ops_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

for required in install.sh preflight.sh verify.sh register-node.sh uninstall.sh smoke-pod.yaml; do
  [[ -f "${ops_dir}/${required}" ]] || { echo "missing ${required}" >&2; exit 1; }
done

require_in() {
  local file="$1"
  local expected="$2"
  grep -Fq -- "${expected}" "${file}" || {
    echo "${file} missing contract: ${expected}" >&2
    exit 1
  }
}

reject_in() {
  local file="$1"
  local forbidden="$2"
  if grep -Fq -- "${forbidden}" "${file}"; then
    echo "${file} contains forbidden contract: ${forbidden}" >&2
    exit 1
  fi
}

for script in "${ops_dir}"/*.sh; do
  require_in "${script}" 'set -euo pipefail'
  reject_in "${script}" 'rm -rf'
  reject_in "${script}" 'docker.io/'
  reject_in "${script}" 'quay.io/'
  reject_in "${script}" 'registry.k8s.io/'
  reject_in "${script}" 'systemctl restart'
  reject_in "${script}" 'service kubelet restart'
  reject_in "${script}" 'storageclass.kubernetes.io/is-default-class=true'
  if grep -Eq 'curl[^|]*\|[[:space:]]*kubectl[[:space:]]+apply' "${script}"; then
    echo "${script} pipes remote content to kubectl" >&2
    exit 1
  fi
done

require_in "${ops_dir}/preflight.sh" '172.28.1.232'
require_in "${ops_dir}/preflight.sh" '172.28.1.233'
require_in "${ops_dir}/preflight.sh" '/data1/ray-cache'
require_in "${ops_dir}/preflight.sh" '/data2/ray-cache'
require_in "${ops_dir}/preflight.sh" 'accelerator=nvidia-rtx-4090'
require_in "${ops_dir}/preflight.sh" 'gpu-pool=production'
require_in "${ops_dir}/preflight.sh" 'servicemonitors.monitoring.coreos.com'
require_in "${ops_dir}/preflight.sh" 'prometheusrules.monitoring.coreos.com'
require_in "${ops_dir}/preflight.sh" 'crictl pull'
require_in "${ops_dir}/preflight.sh" 'may populate the container runtime image cache'
require_in "${ops_dir}/preflight.sh" 'never starts containers'
require_in "${ops_dir}/preflight.sh" 'RAY_CACHE_EXPECTED_ROOT_GID'
require_in "${ops_dir}/preflight.sh" 'expected_ancestor_mode=755'
require_in "${ops_dir}/preflight.sh" 'expected_cache_root_mode=770'
reject_in "${ops_dir}/preflight.sh" 'crictl inspecti'
if grep -Eq 'kubectl[^#]*(apply|create|delete|patch|replace|edit|label|taint)' "${ops_dir}/preflight.sh"; then
  echo 'preflight must not mutate Kubernetes API resources' >&2
  exit 1
fi

require_in "${ops_dir}/install.sh" 'release_name="ray-cache-local"'
require_in "${ops_dir}/install.sh" 'namespace="ray-cache-local"'
require_in "${ops_dir}/install.sh" 'values-vke-production.yaml'
require_in "${ops_dir}/install.sh" '--atomic'
require_in "${ops_dir}/install.sh" '--wait'
require_in "${ops_dir}/install.sh" 'preflight.sh'
reject_in "${ops_dir}/install.sh" '--set'

require_in "${ops_dir}/verify.sh" 'ray-cache-smoke-'
require_in "${ops_dir}/verify.sh" 'trap cleanup EXIT'
require_in "${ops_dir}/verify.sh" '172.28.1.232'
require_in "${ops_dir}/verify.sh" '172.28.1.233'
require_in "${ops_dir}/verify.sh" 'ray-cache-local'
require_in "${ops_dir}/verify.sh" '/data[12]/ray-cache/pvc-'
require_in "${ops_dir}/verify.sh" 'remote_path_check='
reject_in "${ops_dir}/verify.sh" 'test ! -e "${local_path}"'

require_in "${ops_dir}/register-node.sh" 'values-patch.yaml'
require_in "${ops_dir}/register-node.sh" 'acceptance-report.txt'
require_in "${ops_dir}/register-node.sh" '/data1/ray-cache'
require_in "${ops_dir}/register-node.sh" '/data2/ray-cache'
require_in "${ops_dir}/register-node.sh" '172.28.1.232'
require_in "${ops_dir}/register-node.sh" '172.28.1.233'
require_in "${ops_dir}/register-node.sh" 'trap cleanup EXIT'
reject_in "${ops_dir}/register-node.sh" 'helm upgrade'
if grep -Eq 'kubectl[^#]*(apply|create|delete|patch|replace|edit|label|taint)' "${ops_dir}/register-node.sh"; then
  echo 'register-node must not modify cluster resources' >&2
  exit 1
fi

require_in "${ops_dir}/uninstall.sh" '--confirm-empty'
require_in "${ops_dir}/uninstall.sh" 'storageClassName'
require_in "${ops_dir}/uninstall.sh" 'ray-cache-local'
require_in "${ops_dir}/uninstall.sh" 'helm uninstall'
reject_in "${ops_dir}/uninstall.sh" '/data1/ray-cache'
reject_in "${ops_dir}/uninstall.sh" '/data2/ray-cache'

require_in "${ops_dir}/smoke-pod.yaml" 'storageClassName: ray-cache-local'
require_in "${ops_dir}/smoke-pod.yaml" 'automountServiceAccountToken: false'
require_in "${ops_dir}/smoke-pod.yaml" 'privileged: false'
require_in "${ops_dir}/smoke-pod.yaml" 'harbor.wellspiking.ai/guofeng.su/busybox@sha256:ff6bba6f18535e7ccb3c1bbed0b84e5c733d7d9dd8815f1ea93ee73073135aa4'
reject_in "${ops_dir}/smoke-pod.yaml" 'nvidia.com/gpu'

bash "${ops_dir}/test/verify-path-test.sh"
bash "${ops_dir}/test/preflight-owner-test.sh"
bash "${ops_dir}/test/register-node-output-test.sh"

echo 'ray cache operations contract verified'
