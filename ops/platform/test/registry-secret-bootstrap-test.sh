#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly TEMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TEMP_DIR"' EXIT

cat >"${TEMP_DIR}/profile.yaml" <<'EOF'
namespace: ray-train-platform
global:
  imagePullSecrets:
    - name: harbor-registry
postgres:
  mode: external
tos:
  secretName: ""
EOF

cat >"${TEMP_DIR}/secrets.env" <<'EOF'
DATABASE_URL='postgres://platform:example@postgres:5432/platform?sslmode=disable'
PAT_PEPPER='0123456789abcdef0123456789abcdef'
BOOTSTRAP_ADMIN_PASSWORD='example-password'
REGISTRY_SERVER='harbor.wellspiking.ai'
REGISTRY_USERNAME='robot$ray-train'
REGISTRY_PASSWORD='example-registry-password'
EOF
chmod 600 "${TEMP_DIR}/secrets.env"

cat >"${TEMP_DIR}/kubectl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${MOCK_LOG}"
case "$*" in
  "create namespace ray-train-platform "*)
    printf '%s\n' 'apiVersion: v1' 'kind: Namespace' 'metadata:' '  name: ray-train-platform'
    ;;
  "label namespace ray-train-platform "*)
    ;;
  *"create secret generic "*|*"create secret docker-registry "*)
    printf '%s\n' 'apiVersion: v1' 'kind: Secret' 'metadata:' '  name: generated'
    ;;
  "apply -f -"*)
    cat >/dev/null
    ;;
  *)
    echo "unexpected kubectl invocation: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "${TEMP_DIR}/kubectl"

export MOCK_LOG="${TEMP_DIR}/commands.log"
KUBECTL="${TEMP_DIR}/kubectl" bash "${ROOT_DIR}/ops/platform/bootstrap-secrets.sh" \
  --profile "${TEMP_DIR}/profile.yaml" --env-file "${TEMP_DIR}/secrets.env"

grep -Fq 'create secret docker-registry harbor-registry' "${MOCK_LOG}"
grep -Fq -- '--docker-server=harbor.wellspiking.ai' "${MOCK_LOG}"
grep -Fq -- '--docker-username=robot$ray-train' "${MOCK_LOG}"

echo 'registry secret bootstrap contract verified'
