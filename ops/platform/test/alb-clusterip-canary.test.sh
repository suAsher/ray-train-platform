#!/usr/bin/env bash
set -euo pipefail

manifest="${1:-$(dirname "$0")/alb-clusterip-canary.yaml}"

grep -Fq 'type: ClusterIP' "$manifest"
grep -Fq 'targetPort: http' "$manifest"
grep -Fq 'ingressClassName: raytrain-prod-alb' "$manifest"
grep -Fq 'ingress.vke.volcengine.com/loadbalancer-pass-through: "true"' "$manifest"
grep -Fq 'path: /__platform-clusterip-canary' "$manifest"
grep -Fq 'name: ray-train-frontend-clusterip-canary' "$manifest"
