#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly E2E_SCRIPT="${ROOT_DIR}/scripts/e2e_external_spk_rayjob.sh"
readonly NATIVE_E2E_SCRIPT="${ROOT_DIR}/scripts/e2e_native_ray_submit.sh"
readonly PORTAL_E2E_SCRIPT="${ROOT_DIR}/scripts/e2e_portal_submit.sh"
readonly RELEASE_DOCKERFILE="${ROOT_DIR}/backend/Dockerfile.spk-rayjob"

[[ -x "$E2E_SCRIPT" ]] || {
  echo 'external spk-rayjob acceptance script must be executable' >&2
  exit 1
}
grep -Fq -- '--password-stdin' "$E2E_SCRIPT" || {
  echo 'external acceptance must verify the same platform-account login flow' >&2
  exit 1
}
grep -Fq -- '--add-host' "$E2E_SCRIPT" || {
  echo 'external acceptance must support validating an ALB VIP before DNS cutover' >&2
  exit 1
}
grep -Fq 'external-spk-rayjob-e2e.json' "$E2E_SCRIPT" || {
  echo 'external acceptance must verify its output artifact' >&2
  exit 1
}
grep -Fq 'kubectl -n "$namespace" exec "$head_pod"' "$E2E_SCRIPT" || {
  echo 'external acceptance must verify the governed output through the Ray head pod' >&2
  exit 1
}
grep -Eq 'submit( --output json)? --config "\$config"' "$E2E_SCRIPT" || {
  echo 'external acceptance must submit through the short-lived login config' >&2
  exit 1
}
# The CLI now prints readable text by default. Acceptance parses the payload, so
# it must keep asking for the machine-readable form explicitly.
grep -Fq -- '--output json' "$E2E_SCRIPT" || {
  echo 'external acceptance must request machine-readable output before parsing it' >&2
  exit 1
}
if grep -Eiq 'echo.*(password|token|secret)|set -x' "$E2E_SCRIPT"; then
  echo 'external acceptance must not print credentials' >&2
  exit 1
fi

[[ -x "$NATIVE_E2E_SCRIPT" && -x "$PORTAL_E2E_SCRIPT" ]] || {
  echo 'native and Portal acceptance scripts must be executable' >&2
  exit 1
}
grep -Eq 'readonly RAY_CLI_IMAGE=.*@sha256:[0-9a-f]{64}' "$NATIVE_E2E_SCRIPT" || {
  echo 'native acceptance must run the Ray client in a fixed trusted digest image' >&2
  exit 1
}
grep -Fq -- '--metadata-json "$metadata"' "$NATIVE_E2E_SCRIPT" || {
  echo 'native acceptance must pass the workload image and resource shape as strict platform metadata' >&2
  exit 1
}
if grep -Fq 'docker run --rm --env-file "$env_file"' "$NATIVE_E2E_SCRIPT" \
  && grep -Fq -- '-w /source "$image"' "$NATIVE_E2E_SCRIPT"; then
  echo 'native acceptance must not inject the PAT into the user-selected workload image' >&2
  exit 1
fi
for command in jobs status logs; do
  grep -Eq "spk-rayjob ${command} .*--config \"\\\$config\" .*--server \"\\\$api_url\"" "$NATIVE_E2E_SCRIPT" || {
    echo "native acceptance ${command} must use the selected config and server" >&2
    exit 1
  }
done
for command in status logs; do
  grep -Eq "spk-rayjob ${command} .*--config \"\\\$config\" .*--server \"\\\$api_url\"" "$PORTAL_E2E_SCRIPT" || {
    echo "Portal acceptance ${command} must use the selected config and server" >&2
    exit 1
  }
done
if grep -Fq -- '-H "$authorization"' "$PORTAL_E2E_SCRIPT"; then
  echo 'Portal acceptance must not expose the bearer token in curl argv' >&2
  exit 1
fi
if grep -Eq 'curl .*"\$upload_url"' "$PORTAL_E2E_SCRIPT"; then
  echo 'Portal acceptance must not expose presigned upload URLs in curl argv' >&2
  exit 1
fi
grep -Fq 'ray-platform.worker-replicas' "${ROOT_DIR}/docs/SUBMIT_GUIDE.md" || {
  echo 'native multi-GPU documentation must show platform metadata' >&2
  exit 1
}
grep -Fq 'cd /out && sha256sum spk-rayjob-* > SHA256SUMS' "$RELEASE_DOCKERFILE" || {
  echo 'CLI checksum manifest must contain portable basenames, not /out paths' >&2
  exit 1
}

echo 'external spk-rayjob acceptance contract verified'
