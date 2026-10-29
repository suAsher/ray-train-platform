#!/usr/bin/env bash
# GitHub-hosted CI only. Does not prove GPU/Kueue/CSI training readiness.
set -euo pipefail
[[ ${GITHUB_ACTIONS:-} == true ]] || { echo 'This smoke runs only in GitHub Actions' >&2; exit 1; }
[[ $(kubectl config current-context) == kind-raytrain-ci ]] || { echo 'Wrong cluster context' >&2; exit 1; }
cd "$(dirname "${BASH_SOURCE[0]}")/.."
work=$(mktemp -d)
forward_pid=''
cleanup() {
  if [[ -n $forward_pid ]]; then kill "$forward_pid" 2>/dev/null || true; fi
  rm -rf "$work"
}
trap cleanup EXIT
diagnose() {
  kubectl -n raytrain-ci get pods
  kubectl -n raytrain-ci logs deployment/ray-train-backend --tail=100 || true
}
trap diagnose ERR
kind load docker-image local/raytrain:ci --name raytrain-ci
kubectl create namespace raytrain-ci
kubectl -n raytrain-ci create secret generic ray-platform-postgres \
  --from-literal=DATABASE_URL='postgres://platform:ci-database-password@postgres:5432/platform?sslmode=disable' \
  --from-literal=POSTGRES_USER=platform --from-literal=POSTGRES_PASSWORD=ci-database-password
kubectl -n raytrain-ci create secret generic ray-platform-pat --from-literal=PAT_PEPPER=ci-only-pepper-with-at-least-32-bytes
kubectl -n raytrain-ci create secret generic ray-platform-bootstrap-admin --from-literal=BOOTSTRAP_ADMIN_PASSWORD=ci-admin-password-12345
# No training Pods are submitted. Runtime references are inert render fixtures.
runtime='registry.example.test/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
helper='registry.example.test/helper@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
helm upgrade --install raytrain helm/ray-train-platform -n raytrain-ci \
  -f deploy/profiles/quickstart.yaml \
  --set global.imageRegistry=local --set backend.image.repository=raytrain --set backend.image.tag=ci \
  --set backend.workspaceImage="$runtime" --set backend.sourceMaterializerImage="$helper" \
  --set kueue.enabled=false --set kueue.manageResources=false --set kueue.autoQuota=false \
  --set backend.resources.requests.cpu=100m --set backend.resources.requests.memory=128Mi \
  --set postgres.standalone.size=1Gi --wait --timeout 5m
start_forward() {
  kubectl -n raytrain-ci port-forward service/ray-train-backend 18080:8080 >"$work/forward.log" 2>&1 &
  forward_pid=$!
  for attempt in $(seq 1 60); do
    if curl -fsS http://127.0.0.1:18080/readyz > /dev/null; then return; fi
    sleep 1
  done
  return 1
}
start_forward
python3 scripts/ci-web-smoke.py "$work/session.json"
# Restart both processes; the same session must survive PostgreSQL recovery.
kill "$forward_pid"; wait "$forward_pid" || true; forward_pid=''
kubectl -n raytrain-ci delete pod postgres-0
kubectl -n raytrain-ci rollout status statefulset/postgres --timeout=180s
kubectl -n raytrain-ci rollout restart deployment/ray-train-backend
kubectl -n raytrain-ci rollout status deployment/ray-train-backend --timeout=180s
start_forward
python3 scripts/ci-web-smoke.py "$work/session.json" --reuse-session
echo 'Fresh install, embedded UI, local auth, API access and persisted session verified; GPU training not tested.'
