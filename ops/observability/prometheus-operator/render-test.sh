#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly helm_bin="${HELM_BIN:-helm}"
readonly chart="${root_dir}/helm/vendor/kube-prometheus-stack-87.18.1.tgz"
readonly values="${root_dir}/ops/observability/prometheus-operator/20-values-production.yaml"

command -v "$helm_bin" >/dev/null || { echo "missing helm: $helm_bin" >&2; exit 1; }
[[ -f "$chart" && -f "$values" ]] || { echo 'fixed Operator chart or production values are missing' >&2; exit 1; }

rendered="$(mktemp)"
trap 'rm -f "$rendered"' EXIT
"$helm_bin" template prometheus "$chart" --namespace monitoring --values "$values" --include-crds >"$rendered"

grep -q 'name: prometheus-prometheus' "$rendered"
grep -q 'storage: 50Gi' "$rendered"
grep -q 'harbor.wellspiking.ai/guofeng.su/prometheus-operator:v0.92.1@sha256:7d9247d2351480fc74587e24681578f815f387bafb2ee7b86a852a94c4cd3774' "$rendered"
grep -q 'harbor.wellspiking.ai/guofeng.su/prometheus-config-reloader:v0.92.1@sha256:74550ba3e8bf93f47bc574231090d340ae9c01d25cd11ff74799e65f9fdb9a48' "$rendered"
grep -q 'harbor.wellspiking.ai/guofeng.su/thanos:v0.42.2@sha256:6249f7aaadd3695df637fb2eb4cb9a9955611eee691c3970892fe9c0dc3f2db6' "$rendered"
! grep -q 'ray-observability' "$rendered"
if grep -Eq '(^[[:space:]]*image:.*(quay\.io|ghcr\.io|docker\.io)|--[a-z-]+=.*(quay\.io|ghcr\.io|docker\.io))' "$rendered"; then
  echo 'rendered observability chart still contains an external workload image reference' >&2
  exit 1
fi

echo 'Prometheus Operator production chart rendered successfully'
