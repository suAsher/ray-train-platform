#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly TEMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TEMP_DIR"' EXIT

cat >"${TEMP_DIR}/profile.yaml" <<'EOF'
namespace: ray-train-platform
kueue:
  manageResources: true
  clusterQueueName: ray-platform-test-gpu
  resourceFlavorName: ray-platform-test-gpu
EOF

cat >"${TEMP_DIR}/kubectl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${MOCK_LOG}"
if [[ " $* " == *" delete "* ]]; then
  echo 'unexpected delete during preview' >&2
  exit 99
fi
case "$*" in
  *"get namespaces -l app.kubernetes.io/part-of=ray-train-platform"*)
    printf 'tenant-platform\nray-train-platform\n'
    ;;
  *"get namespaces -l app.kubernetes.io/managed-by=ray-train-platform"*)
    printf 'tenant-legacy\n'
    ;;
  *"get pv -l app.kubernetes.io/part-of=ray-train-platform"*)
    printf 'ray-data-owned\n'
    ;;
  *"-n ray-train-platform get pvc"*)
    printf 'data-postgres-0 pvc-platform-postgres\n'
    ;;
  *"get pv ray-train-local-datasets"*)
    printf 'persistentvolume/ray-train-local-datasets\n'
    ;;
  *"get pv "*) exit 1 ;;
  *"get localqueues -A"*)
    printf 'tenant-platform local-gpu ray-platform-test-gpu\n'
    ;;
  *"get clusterqueue ray-platform-test-gpu -o name"*)
    printf 'clusterqueue.kueue.x-k8s.io/ray-platform-test-gpu\n'
    ;;
  *"get clusterqueues -l app.kubernetes.io/part-of=ray-train-platform"*)
    printf 'ray-platform-test-gpu\n'
    ;;
  *"get resourceflavor ray-platform-test-gpu"*)
    printf 'resourceflavor.kueue.x-k8s.io/ray-platform-test-gpu\n'
    ;;
  *"get namespace ray-train-platform"*)
    printf 'namespace/ray-train-platform\n'
    ;;
esac
EOF
chmod +x "${TEMP_DIR}/kubectl"

cat >"${TEMP_DIR}/helm" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${MOCK_LOG}"
if [[ " $* " == *" uninstall "* ]]; then
  echo 'unexpected uninstall during preview' >&2
  exit 99
fi
EOF
chmod +x "${TEMP_DIR}/helm"

export MOCK_LOG="${TEMP_DIR}/commands.log"
output="$(KUBECTL="${TEMP_DIR}/kubectl" HELM="${TEMP_DIR}/helm" bash "${ROOT_DIR}/ops/platform/reset.sh" --profile "${TEMP_DIR}/profile.yaml" --include-legacy-platform-resources)"

grep -Fq 'PREVIEW ONLY' <<<"$output"
grep -Fq 'tenant-platform' <<<"$output"
grep -Fq 'tenant-legacy' <<<"$output"
grep -Fq 'ray-data-owned' <<<"$output"
grep -Fq 'ray-train-local-datasets' <<<"$output"
if grep -Eq '(^| )(delete|uninstall)( |$)' "${TEMP_DIR}/commands.log"; then
  echo 'preview issued a destructive command' >&2
  exit 1
fi

echo 'reset preview contract verified'
