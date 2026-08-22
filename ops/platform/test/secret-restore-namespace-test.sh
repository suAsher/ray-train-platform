#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly TEMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TEMP_DIR"' EXIT

cat >"${TEMP_DIR}/profile.yaml" <<'EOF'
namespace: ray-train-platform
EOF

mkdir -p "${TEMP_DIR}/state/secrets"
chmod 700 "${TEMP_DIR}/state"
cat >"${TEMP_DIR}/state/secrets/platform-secret.yaml" <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: platform-secret
  namespace: default
type: Opaque
data:
  value: Zm9v
EOF

cat >"${TEMP_DIR}/kubectl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == "create namespace ray-train-platform "* ]]; then
  cat <<'YAML'
apiVersion: v1
kind: Namespace
metadata:
  name: ray-train-platform
YAML
  exit 0
fi
if [[ "$*" == "label namespace ray-train-platform "* ]]; then
  exit 0
fi
if [[ "$*" == "create -f "* ]]; then
  cat <<'JSON'
{"apiVersion":"v1","kind":"Secret","metadata":{"name":"platform-secret","namespace":"default"},"type":"Opaque","data":{"value":"Zm9v"}}
JSON
  exit 0
fi
if [[ "$*" == "apply -f -" ]]; then
  cat >"${MOCK_APPLIED_MANIFEST}"
  exit 0
fi
if [[ "$*" == "apply -f -"* ]]; then
  cat >/dev/null
  exit 0
fi
echo "unexpected kubectl invocation: $*" >&2
exit 1
EOF
chmod +x "${TEMP_DIR}/kubectl"

export MOCK_APPLIED_MANIFEST="${TEMP_DIR}/applied.yaml"
KUBECTL="${TEMP_DIR}/kubectl" bash "${ROOT_DIR}/ops/platform/restore-secrets.sh" \
  --profile "${TEMP_DIR}/profile.yaml" --state-dir "${TEMP_DIR}/state"

jq -e '.metadata.namespace == "ray-train-platform"' "${MOCK_APPLIED_MANIFEST}" >/dev/null
if jq -e '.metadata.namespace == "default"' "${MOCK_APPLIED_MANIFEST}" >/dev/null; then
  echo 'restore did not override the backup namespace' >&2
  exit 1
fi

echo 'secret restore namespace contract verified'
