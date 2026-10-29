#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
chart=helm/ray-train-platform
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
runtime=registry.example.com/test/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
helper=registry.example.com/test/helper@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
args=(-f deploy/profiles/quickstart.yaml --set backend.workspaceImage="$runtime" --set backend.sourceMaterializerImage="$helper")
helm lint "$chart" "${args[@]}"
helm template portable "$chart" -n portable-test "${args[@]}" > "$tmp/default.yaml"
grep -q 'name: ray-train-backend' "$tmp/default.yaml"
grep -q 'namespace: portable-test' "$tmp/default.yaml"
grep -q 'name: PAT_PEPPER' "$tmp/default.yaml"
grep -q 'name: LOCAL_AUTH_ENABLED' "$tmp/default.yaml"
grep -q 'kind: ClusterQueue' "$tmp/default.yaml"
grep -q 'kind: WorkloadPriorityClass' "$tmp/default.yaml"
if grep -Eq 'wellspiking|IDC_SYNC|STORAGE_SYNC|ray-train-frontend|kind: ALBInstance|hostPath:' "$tmp/default.yaml"; then
  echo 'Private integration or separate frontend leaked into portable chart' >&2; exit 1
fi
helm template portable "$chart" -n portable-test "${args[@]}" --set ingress.enabled=true --set ingress.host=training.example.com > "$tmp/ingress.yaml"
grep -q 'kind: Ingress' "$tmp/ingress.yaml"
grep -q 'number: 8080' "$tmp/ingress.yaml"
if helm template portable "$chart" "${args[@]}" --set backend.sourceMaterializerImage=unsafe:latest > /dev/null 2>&1; then
  echo 'Unpinned helper image was accepted' >&2; exit 1
fi
if helm template portable "$chart" "${args[@]}" --set kueue.topology.teamNodeAffinityEnabled=true > /dev/null 2>&1; then
  echo 'Team affinity without TAS was accepted' >&2; exit 1
fi
echo 'Portable Helm contract passed'
