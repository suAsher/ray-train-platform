#!/usr/bin/env bash
set -euo pipefail

readonly TEST_ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"

# shellcheck source=ops/mlflow/deploy.sh
source "${TEST_ROOT_DIR}/ops/mlflow/deploy.sh"

fsx_credentials_type="irsa"
fsx_role="trn:iam::2103446203:role/tos-rw"
fsx_desired=5
fsx_available=5
csidriver_exists=true

kubectl() {
  if [[ "$*" == "get csidriver fsx.csi.volcengine.com" ]]; then
    [[ "$csidriver_exists" == true ]]
    return
  fi
  if [[ "$*" == "-n kube-system get daemonset csi-fsx-node -o json" ]]; then
    jq -n \
      --arg credentials_type "$fsx_credentials_type" \
      --arg role "$fsx_role" \
      --argjson desired "$fsx_desired" \
      --argjson available "$fsx_available" '
        {
          status: {
            desiredNumberScheduled: $desired,
            numberAvailable: $available
          },
          spec: {
            template: {
              spec: {
                containers: [{
                  name: "driver",
                  env: [
                    {name: "CREDENTIALS_TYPE", value: $credentials_type},
                    {name: "ROLE_NAME_FOR_IRSA", value: $role}
                  ]
                }]
              }
            }
          }
        }
      '
    return
  fi
  echo "unexpected kubectl invocation: $*" >&2
  return 1
}

verify_fsx_irsa

fsx_credentials_type="Secret"
if verify_fsx_irsa >/dev/null 2>&1; then
  echo 'FSX preflight accepted a non-IRSA credential type' >&2
  exit 1
fi

fsx_credentials_type="IrSa"
fsx_role=""
if verify_fsx_irsa >/dev/null 2>&1; then
  echo 'FSX preflight accepted an empty IRSA role' >&2
  exit 1
fi

fsx_role="trn:iam::2103446203:role/tos-rw"
fsx_available=4
if verify_fsx_irsa >/dev/null 2>&1; then
  echo 'FSX preflight accepted a degraded node DaemonSet' >&2
  exit 1
fi

fsx_available=5
csidriver_exists=false
if verify_fsx_irsa >/dev/null 2>&1; then
  echo 'FSX preflight accepted a missing CSIDriver' >&2
  exit 1
fi

echo 'MLflow FSX IRSA preflight contract verified'
