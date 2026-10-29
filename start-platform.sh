#!/usr/bin/env bash
set -euo pipefail
cat <<'HELP'
RayTrain is installed with Helm; this script does not modify a cluster.

1. Read README.md and docs/DEPLOYMENT.md.
2. Build the unified platform, source-materializer and workspace images.
3. Configure deploy/profiles/quickstart.yaml, Secrets, KubeRay and Kueue.
4. Render and inspect the chart, then run helm upgrade --install explicitly.
HELP
