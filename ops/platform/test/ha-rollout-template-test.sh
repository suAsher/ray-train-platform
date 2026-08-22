#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)"
VALUES="${ROOT}/helm/ray-train-platform/values.yaml"
PROFILE="${ROOT}/deploy/profiles/vke-cpu-ha.yaml"

grep -F 'rollingUpdate:' "$VALUES" >/dev/null
grep -F 'maxUnavailable: 0' "$VALUES" >/dev/null
grep -F 'maxSurge: 1' "$VALUES" >/dev/null
grep -F '.Values.availability.rollingUpdate.maxUnavailable' "${ROOT}/helm/ray-train-platform/templates/backend-deployment.yaml" >/dev/null
grep -F '.Values.availability.rollingUpdate.maxSurge' "${ROOT}/helm/ray-train-platform/templates/backend-deployment.yaml" >/dev/null
grep -F '.Values.availability.rollingUpdate.maxUnavailable' "${ROOT}/helm/ray-train-platform/templates/frontend-deployment.yaml" >/dev/null
grep -F '.Values.availability.rollingUpdate.maxSurge' "${ROOT}/helm/ray-train-platform/templates/frontend-deployment.yaml" >/dev/null

awk '
  /^availability:/ { in_availability=1; next }
  in_availability && /^  rollingUpdate:/ { in_rolling=1; next }
  in_rolling && /^    maxUnavailable: 1$/ { has_unavailable=1 }
  in_rolling && /^    maxSurge: 0$/ { has_surge=1 }
  END { exit !(has_unavailable && has_surge) }
' "$PROFILE"

echo "HA rollout template contract passed"
