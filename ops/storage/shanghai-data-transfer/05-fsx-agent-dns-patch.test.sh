#!/usr/bin/env bash
set -Eeuo pipefail

patch_file="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/05-fsx-agent-dns-patch.json"

jq -e '
  (
    (.spec.template.podTemplateSpec.spec.dnsPolicy == "ClusterFirstWithHostNet")
    and (.spec.upgradeStrategy.type == "RollingUpdate")
    and (.spec.upgradeStrategy.maxUnavailable == "1")
    and (.spec.upgradeStrategy.maxSurge == "0")
  )
' "$patch_file" >/dev/null
